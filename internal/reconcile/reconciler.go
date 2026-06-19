package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mcmoney/platform-control-plane/internal/domain"
)

type Config struct {
	GitOpsRepoPath string
	ClusterName    string
	GitOpsRepoURL  string
	GitAuthorName  string
	GitAuthorEmail string
	GitBranch      string
	GitRemoteName  string
	GitCommit      bool
	GitPush        bool
	KubeApply      bool
	KubeconfigPath string
}

type Result struct {
	Namespace    string
	GitOpsPath   string
	GitCommitSHA string
	GitBranch    string
}

type Reconciler interface {
	Reconcile(context.Context, domain.EnvironmentRequest, domain.EnvironmentClass) (Result, error)
	Ready(context.Context) error
}

type GitOpsKubernetesReconciler struct {
	logger *slog.Logger
	cfg    Config
	client kubernetes.Interface
}

func NewGitOpsKubernetesReconciler(logger *slog.Logger, cfg Config) (*GitOpsKubernetesReconciler, error) {
	r := &GitOpsKubernetesReconciler{
		logger: logger,
		cfg:    cfg,
	}

	if cfg.KubeApply {
		client, err := newClientset(cfg.KubeconfigPath)
		if err != nil {
			return nil, err
		}
		r.client = client
	}

	return r, nil
}

func (r *GitOpsKubernetesReconciler) Reconcile(ctx context.Context, req domain.EnvironmentRequest, class domain.EnvironmentClass) (Result, error) {
	namespace := buildNamespace(req)
	relativePath := filepath.Join("clusters", req.Region, "teams", req.Team, req.App, req.ID)
	absolutePath := filepath.Join(r.cfg.GitOpsRepoPath, relativePath)

	if err := os.MkdirAll(absolutePath, 0o755); err != nil {
		return Result{}, fmt.Errorf("create gitops path: %w", err)
	}

	files := map[string]string{
		"namespace.yaml":          renderYAML(namespaceManifest(namespace, req)),
		"resourcequota.yaml":      renderYAML(resourceQuotaManifest(namespace, class)),
		"networkpolicy.yaml":      renderYAML(networkPolicyManifest(namespace)),
		"argocd-application.yaml": renderYAML(argocdApplicationManifest(r.cfg, namespace, relativePath, req)),
	}

	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(absolutePath, name), []byte(contents), 0o644); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", name, err)
		}
	}

	if r.client != nil {
		if err := r.applyToCluster(ctx, namespace, req, class); err != nil {
			return Result{}, err
		}
	}

	commitSHA, err := r.publishGitOps(ctx, relativePath, req)
	if err != nil {
		return Result{}, err
	}

	r.logger.Info("environment_reconciled",
		"request_id", req.ID,
		"namespace", namespace,
		"gitops_path", absolutePath,
		"git_commit_sha", commitSHA,
		"kube_apply", r.client != nil,
	)

	return Result{
		Namespace:    namespace,
		GitOpsPath:   absolutePath,
		GitCommitSHA: commitSHA,
		GitBranch:    r.cfg.GitBranch,
	}, nil
}

func (r *GitOpsKubernetesReconciler) Ready(ctx context.Context) error {
	if err := os.MkdirAll(r.cfg.GitOpsRepoPath, 0o755); err != nil {
		return fmt.Errorf("ensure gitops repo path: %w", err)
	}
	testFile := filepath.Join(r.cfg.GitOpsRepoPath, ".platform-ready")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("write gitops readiness file: %w", err)
	}
	_ = os.Remove(testFile)

	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git binary unavailable: %w", err)
	}
	if err := r.ensureGitRepo(ctx); err != nil {
		return err
	}
	if err := r.ensureGitRemote(ctx); err != nil {
		return err
	}
	if r.cfg.GitPush {
		if strings.TrimSpace(r.cfg.GitOpsRepoURL) == "" {
			return fmt.Errorf("git push enabled but PLATFORM_GITOPS_REPO_URL is empty")
		}
	}
	if r.client != nil {
		if _, err := r.client.Discovery().ServerVersion(); err != nil {
			return fmt.Errorf("kubernetes api readiness probe failed: %w", err)
		}
	}
	return nil
}

func (r *GitOpsKubernetesReconciler) publishGitOps(ctx context.Context, relativePath string, req domain.EnvironmentRequest) (string, error) {
	if !r.cfg.GitCommit {
		return "", nil
	}

	if err := os.MkdirAll(r.cfg.GitOpsRepoPath, 0o755); err != nil {
		return "", fmt.Errorf("create gitops repo path: %w", err)
	}
	if err := r.ensureGitRepo(ctx); err != nil {
		return "", err
	}
	if err := r.ensureGitRemote(ctx); err != nil {
		return "", err
	}

	if _, err := r.runGit(ctx, "add", relativePath); err != nil {
		return "", err
	}

	status, err := r.runGit(ctx, "status", "--porcelain", "--", relativePath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		sha, headErr := r.currentCommitSHA(ctx)
		if headErr != nil {
			return "", nil
		}
		return sha, nil
	}

	message := fmt.Sprintf("reconcile %s for %s/%s", req.ID, req.Team, req.App)
	if _, err := r.runGit(ctx,
		"-c", "user.name="+r.cfg.GitAuthorName,
		"-c", "user.email="+r.cfg.GitAuthorEmail,
		"commit", "-m", message,
	); err != nil {
		return "", err
	}

	sha, err := r.currentCommitSHA(ctx)
	if err != nil {
		return "", err
	}

	if r.cfg.GitPush {
		if _, err := r.runGit(ctx, "push", r.cfg.GitRemoteName, "HEAD:"+r.cfg.GitBranch); err != nil {
			return "", err
		}
	}

	return sha, nil
}

func (r *GitOpsKubernetesReconciler) ensureGitRepo(ctx context.Context) error {
	if _, err := r.runGit(ctx, "rev-parse", "--show-toplevel"); err == nil {
		return nil
	}
	_, err := r.runGit(ctx, "init", "-b", r.cfg.GitBranch)
	if err != nil {
		return err
	}
	return nil
}

func (r *GitOpsKubernetesReconciler) ensureGitRemote(ctx context.Context) error {
	if !r.cfg.GitPush || strings.TrimSpace(r.cfg.GitOpsRepoURL) == "" {
		return nil
	}

	current, err := r.runGit(ctx, "remote", "get-url", r.cfg.GitRemoteName)
	if err == nil {
		if strings.TrimSpace(current) == strings.TrimSpace(r.cfg.GitOpsRepoURL) {
			return nil
		}
		_, err = r.runGit(ctx, "remote", "set-url", r.cfg.GitRemoteName, r.cfg.GitOpsRepoURL)
		return err
	}

	_, err = r.runGit(ctx, "remote", "add", r.cfg.GitRemoteName, r.cfg.GitOpsRepoURL)
	return err
}

func (r *GitOpsKubernetesReconciler) currentCommitSHA(ctx context.Context) (string, error) {
	output, err := r.runGit(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (r *GitOpsKubernetesReconciler) runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.cfg.GitOpsRepoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (r *GitOpsKubernetesReconciler) applyToCluster(ctx context.Context, namespace string, req domain.EnvironmentRequest, class domain.EnvironmentClass) error {
	labels := map[string]string{
		"platform.example.com/team":  req.Team,
		"platform.example.com/app":   req.App,
		"platform.example.com/class": req.Class,
	}

	nsClient := r.client.CoreV1().Namespaces()
	existingNS, err := nsClient.Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		merged := existingNS.DeepCopy()
		if merged.Labels == nil {
			merged.Labels = map[string]string{}
		}
		for key, value := range labels {
			merged.Labels[key] = value
		}
		if _, err := nsClient.Update(ctx, merged, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update namespace %s: %w", namespace, err)
		}
	} else {
		if _, err := nsClient.Create(ctx, namespaceManifest(namespace, req), metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create namespace %s: %w", namespace, err)
		}
	}

	rqClient := r.client.CoreV1().ResourceQuotas(namespace)
	rq := resourceQuotaManifest(namespace, class)
	if current, err := rqClient.Get(ctx, rq.Name, metav1.GetOptions{}); err == nil {
		updated := current.DeepCopy()
		updated.Spec = rq.Spec
		if _, err := rqClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update resource quota %s/%s: %w", namespace, rq.Name, err)
		}
	} else {
		if _, err := rqClient.Create(ctx, rq, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create resource quota %s/%s: %w", namespace, rq.Name, err)
		}
	}

	npClient := r.client.NetworkingV1().NetworkPolicies(namespace)
	np := networkPolicyManifest(namespace)
	if current, err := npClient.Get(ctx, np.Name, metav1.GetOptions{}); err == nil {
		updated := current.DeepCopy()
		updated.Spec = np.Spec
		if _, err := npClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update network policy %s/%s: %w", namespace, np.Name, err)
		}
	} else {
		if _, err := npClient.Create(ctx, np, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create network policy %s/%s: %w", namespace, np.Name, err)
		}
	}

	return nil
}

func newClientset(kubeconfigPath string) (kubernetes.Interface, error) {
	var cfg *rest.Config
	var err error

	if kubeconfigPath != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		cfg, err = rest.InClusterConfig()
		if err != nil {
			cfg, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("build kubernetes config: %w", err)
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	return client, nil
}

func buildNamespace(req domain.EnvironmentRequest) string {
	suffix := req.ID
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}

	return sanitizeDNSLabel(fmt.Sprintf("%s-%s-%s", req.Team, req.App, suffix))
}

func sanitizeDNSLabel(in string) string {
	re := regexp.MustCompile(`[^a-z0-9-]+`)
	value := strings.ToLower(in)
	value = strings.ReplaceAll(value, "_", "-")
	value = re.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if len(value) > 63 {
		value = value[:63]
	}
	return strings.Trim(value, "-")
}

func namespaceManifest(namespace string, req domain.EnvironmentRequest) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Namespace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"platform.example.com/team":  req.Team,
				"platform.example.com/app":   req.App,
				"platform.example.com/class": req.Class,
			},
		},
	}
}

func resourceQuotaManifest(namespace string, class domain.EnvironmentClass) *corev1.ResourceQuota {
	hard := corev1.ResourceList{
		corev1.ResourcePods:                   resourceQuantity(strconv.Itoa(class.DefaultNamespaces * 10)),
		corev1.ResourceServices:               resourceQuantity(strconv.Itoa(class.DefaultNamespaces * 4)),
		corev1.ResourcePersistentVolumeClaims: resourceQuantity(strconv.Itoa(class.DefaultNamespaces * 4)),
		corev1.ResourceRequestsCPU:            resourceQuantity(fmt.Sprintf("%d", class.DefaultNamespaces*4)),
		corev1.ResourceRequestsMemory:         resourceQuantity(fmt.Sprintf("%dGi", class.DefaultNamespaces*8)),
		corev1.ResourceLimitsCPU:              resourceQuantity(fmt.Sprintf("%d", class.DefaultNamespaces*8)),
		corev1.ResourceLimitsMemory:           resourceQuantity(fmt.Sprintf("%dGi", class.DefaultNamespaces*16)),
	}

	return &corev1.ResourceQuota{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ResourceQuota",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "platform-defaults",
			Namespace: namespace,
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: hard,
		},
	}
}

func networkPolicyManifest(namespace string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.k8s.io/v1",
			Kind:       "NetworkPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default-deny-ingress",
			Namespace: namespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
}

func argocdApplicationManifest(cfg Config, namespace, relativePath string, req domain.EnvironmentRequest) map[string]any {
	repoURL := cfg.GitOpsRepoURL
	if repoURL == "" {
		repoURL = req.Repository
	}

	return map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      sanitizeDNSLabel(req.App + "-" + req.Class),
			"namespace": "argocd",
			"labels": map[string]string{
				"platform.example.com/team": req.Team,
			},
		},
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{
				"repoURL":        repoURL,
				"path":           relativePath,
				"targetRevision": req.Revision,
			},
			"destination": map[string]any{
				"server":    "https://kubernetes.default.svc",
				"namespace": namespace,
			},
			"syncPolicy": map[string]any{
				"automated": map[string]bool{
					"prune":    true,
					"selfHeal": true,
				},
				"syncOptions": []string{
					"CreateNamespace=false",
				},
			},
		},
	}
}

func renderYAML(v any) string {
	payload, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Sprintf("marshal error: %v\n", err)
	}
	return string(payload)
}

func resourceQuantity(value string) resource.Quantity {
	return resource.MustParse(value)
}
