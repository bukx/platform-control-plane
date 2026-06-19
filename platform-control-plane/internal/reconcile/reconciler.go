package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mcmoney/platform-control-plane/internal/domain"
)

type Config struct {
	GitOpsRepoPath         string
	ClusterName            string
	GitOpsRepoURL          string
	GitAuthorName          string
	GitAuthorEmail         string
	GitBranch              string
	GitBaseBranch          string
	GitRemoteName          string
	GitCommit              bool
	GitPush                bool
	GitPromotionMode       string
	GitPromotionBranchPref string
	GitProvider            string
	GitHubRepo             string
	GitPRCreate            bool
	KubeApply              bool
	KubeconfigPath         string
}

type Result struct {
	Namespace          string
	GitOpsPath         string
	GitCommitSHA       string
	GitBranch          string
	GitPromotionMode   string
	GitPromotionBranch string
	GitPromotionURL    string
	ClusterStatus      string
	DriftStatus        string
	DriftSummary       string
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

type clusterFeedback struct {
	ClusterStatus string
	DriftStatus   string
	DriftSummary  string
}

type gitPublishResult struct {
	CommitSHA       string
	Branch          string
	PromotionMode   string
	PromotionBranch string
	PromotionURL    string
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
		"limitrange.yaml":         renderYAML(limitRangeManifest(namespace, class)),
		"policypack.yaml":         renderYAML(policyPackManifest(namespace, class)),
		"networkpolicy.yaml":      renderYAML(networkPolicyManifest(namespace)),
		"argocd-application.yaml": renderYAML(argocdApplicationManifest(r.cfg, namespace, relativePath, req)),
	}

	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(absolutePath, name), []byte(contents), 0o644); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", name, err)
		}
	}

	feedback := clusterFeedback{
		ClusterStatus: "gitops_rendered",
		DriftStatus:   "not_checked",
	}
	if r.client != nil {
		var err error
		feedback, err = r.applyToCluster(ctx, namespace, req, class)
		if err != nil {
			return Result{}, err
		}
	}

	publishResult, err := r.publishGitOps(ctx, relativePath, req)
	if err != nil {
		return Result{}, err
	}

	r.logger.Info("environment_reconciled",
		"request_id", req.ID,
		"namespace", namespace,
		"gitops_path", absolutePath,
		"git_commit_sha", publishResult.CommitSHA,
		"git_promotion_mode", publishResult.PromotionMode,
		"git_promotion_branch", publishResult.PromotionBranch,
		"cluster_status", feedback.ClusterStatus,
		"drift_status", feedback.DriftStatus,
		"kube_apply", r.client != nil,
	)

	return Result{
		Namespace:          namespace,
		GitOpsPath:         absolutePath,
		GitCommitSHA:       publishResult.CommitSHA,
		GitBranch:          publishResult.Branch,
		GitPromotionMode:   publishResult.PromotionMode,
		GitPromotionBranch: publishResult.PromotionBranch,
		GitPromotionURL:    publishResult.PromotionURL,
		ClusterStatus:      feedback.ClusterStatus,
		DriftStatus:        feedback.DriftStatus,
		DriftSummary:       feedback.DriftSummary,
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
	if r.cfg.GitPromotionMode == "pull_request" && r.cfg.GitPRCreate {
		if _, err := exec.LookPath("gh"); err != nil {
			return fmt.Errorf("gh cli unavailable for pull-request promotion: %w", err)
		}
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

func (r *GitOpsKubernetesReconciler) publishGitOps(ctx context.Context, relativePath string, req domain.EnvironmentRequest) (gitPublishResult, error) {
	if !r.cfg.GitCommit {
		return gitPublishResult{
			Branch:        r.cfg.GitBaseBranch,
			PromotionMode: r.cfg.GitPromotionMode,
		}, nil
	}

	if err := os.MkdirAll(r.cfg.GitOpsRepoPath, 0o755); err != nil {
		return gitPublishResult{}, fmt.Errorf("create gitops repo path: %w", err)
	}
	if err := r.ensureGitRepo(ctx); err != nil {
		return gitPublishResult{}, err
	}
	if err := r.ensureGitRemote(ctx); err != nil {
		return gitPublishResult{}, err
	}
	baseBranch := fallbackBranch(r.cfg.GitBaseBranch, r.cfg.GitBranch)
	targetBranch := fallbackBranch(r.cfg.GitBranch, baseBranch)
	promotionBranch := targetBranch
	if r.cfg.GitPromotionMode == "pull_request" {
		promotionBranch = fmt.Sprintf("%s/%s", sanitizeBranchSegment(r.cfg.GitPromotionBranchPref), sanitizeBranchSegment(req.ID))
	}
	if err := r.checkoutBranch(ctx, baseBranch, promotionBranch); err != nil {
		return gitPublishResult{}, err
	}

	if _, err := r.runGit(ctx, "add", relativePath); err != nil {
		return gitPublishResult{}, err
	}

	status, err := r.runGit(ctx, "status", "--porcelain", "--", relativePath)
	if err != nil {
		return gitPublishResult{}, err
	}
	if strings.TrimSpace(status) == "" {
		sha, headErr := r.currentCommitSHA(ctx)
		if headErr != nil {
			return gitPublishResult{
				Branch:          promotionBranch,
				PromotionMode:   r.cfg.GitPromotionMode,
				PromotionBranch: promotionBranch,
			}, nil
		}
		return gitPublishResult{
			CommitSHA:       sha,
			Branch:          promotionBranch,
			PromotionMode:   r.cfg.GitPromotionMode,
			PromotionBranch: promotionBranch,
		}, nil
	}

	message := fmt.Sprintf("reconcile %s for %s/%s", req.ID, req.Team, req.App)
	if _, err := r.runGit(ctx,
		"-c", "user.name="+r.cfg.GitAuthorName,
		"-c", "user.email="+r.cfg.GitAuthorEmail,
		"commit", "-m", message,
	); err != nil {
		return gitPublishResult{}, err
	}

	sha, err := r.currentCommitSHA(ctx)
	if err != nil {
		return gitPublishResult{}, err
	}

	prURL := ""
	if r.cfg.GitPush {
		pushBranch := targetBranch
		if r.cfg.GitPromotionMode == "pull_request" {
			pushBranch = promotionBranch
		}
		if _, err := r.runGit(ctx, "push", r.cfg.GitRemoteName, "HEAD:"+pushBranch); err != nil {
			return gitPublishResult{}, err
		}
		if r.cfg.GitPromotionMode == "pull_request" && r.cfg.GitPRCreate {
			prURL, err = r.createOrUpdatePullRequest(ctx, promotionBranch, baseBranch, req)
			if err != nil {
				return gitPublishResult{}, err
			}
		}
	}

	return gitPublishResult{
		CommitSHA:       sha,
		Branch:          promotionBranch,
		PromotionMode:   r.cfg.GitPromotionMode,
		PromotionBranch: promotionBranch,
		PromotionURL:    prURL,
	}, nil
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

func (r *GitOpsKubernetesReconciler) checkoutBranch(ctx context.Context, baseBranch, targetBranch string) error {
	if _, err := r.runGit(ctx, "checkout", "-B", baseBranch); err != nil {
		return err
	}
	if targetBranch == "" || targetBranch == baseBranch {
		return nil
	}
	_, err := r.runGit(ctx, "checkout", "-B", targetBranch)
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

func (r *GitOpsKubernetesReconciler) createOrUpdatePullRequest(ctx context.Context, branch, base string, req domain.EnvironmentRequest) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("gh cli is required for pull-request promotion: %w", err)
	}
	repo := strings.TrimSpace(r.cfg.GitHubRepo)
	if repo == "" {
		repo = inferGitHubRepo(r.cfg.GitOpsRepoURL)
	}
	if repo == "" {
		return "", fmt.Errorf("unable to resolve GitHub repo for pull-request promotion")
	}

	existing, err := r.runCommand(ctx, "gh", "pr", "list", "--repo", repo, "--head", branch, "--base", base, "--json", "url", "--jq", ".[0].url")
	if err == nil && strings.TrimSpace(existing) != "" {
		return strings.TrimSpace(existing), nil
	}

	title := fmt.Sprintf("Promote %s for %s/%s", req.ID, req.Team, req.App)
	body := fmt.Sprintf("Automated GitOps promotion for request `%s`.\n\n- team: `%s`\n- app: `%s`\n- class: `%s`\n- region: `%s`\n- revision: `%s`\n", req.ID, req.Team, req.App, req.Class, req.Region, req.Revision)
	url, err := r.runCommand(ctx, "gh", "pr", "create", "--repo", repo, "--base", base, "--head", branch, "--title", title, "--body", body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(url), nil
}

func (r *GitOpsKubernetesReconciler) runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.cfg.GitOpsRepoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (r *GitOpsKubernetesReconciler) applyToCluster(ctx context.Context, namespace string, req domain.EnvironmentRequest, class domain.EnvironmentClass) (clusterFeedback, error) {
	feedback := clusterFeedback{
		ClusterStatus: "applied",
		DriftStatus:   "in_sync",
	}
	driftDetails := make([]string, 0, 6)
	labels := map[string]string{
		"platform.example.com/team":  req.Team,
		"platform.example.com/app":   req.App,
		"platform.example.com/class": req.Class,
	}

	nsClient := r.client.CoreV1().Namespaces()
	existingNS, err := nsClient.Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		merged := existingNS.DeepCopy()
		if !reflect.DeepEqual(normalizeLabelSubset(existingNS.Labels, labels), labels) {
			driftDetails = append(driftDetails, "namespace labels differed")
		}
		if merged.Labels == nil {
			merged.Labels = map[string]string{}
		}
		for key, value := range labels {
			merged.Labels[key] = value
		}
		if _, err := nsClient.Update(ctx, merged, metav1.UpdateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("update namespace %s: %w", namespace, err)
		}
	} else if k8serrors.IsNotFound(err) {
		driftDetails = append(driftDetails, "namespace missing")
		if _, err := nsClient.Create(ctx, namespaceManifest(namespace, req), metav1.CreateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("create namespace %s: %w", namespace, err)
		}
	} else {
		return clusterFeedback{}, fmt.Errorf("get namespace %s: %w", namespace, err)
	}

	rqClient := r.client.CoreV1().ResourceQuotas(namespace)
	rq := resourceQuotaManifest(namespace, class)
	if current, err := rqClient.Get(ctx, rq.Name, metav1.GetOptions{}); err == nil {
		if !reflect.DeepEqual(current.Spec.Hard, rq.Spec.Hard) {
			driftDetails = append(driftDetails, "resource quota differed")
		}
		updated := current.DeepCopy()
		updated.Spec = rq.Spec
		if _, err := rqClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("update resource quota %s/%s: %w", namespace, rq.Name, err)
		}
	} else if k8serrors.IsNotFound(err) {
		driftDetails = append(driftDetails, "resource quota missing")
		if _, err := rqClient.Create(ctx, rq, metav1.CreateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("create resource quota %s/%s: %w", namespace, rq.Name, err)
		}
	} else {
		return clusterFeedback{}, fmt.Errorf("get resource quota %s/%s: %w", namespace, rq.Name, err)
	}

	lrClient := r.client.CoreV1().LimitRanges(namespace)
	lr := limitRangeManifest(namespace, class)
	if current, err := lrClient.Get(ctx, lr.Name, metav1.GetOptions{}); err == nil {
		if !reflect.DeepEqual(current.Spec, lr.Spec) {
			driftDetails = append(driftDetails, "limit range differed")
		}
		updated := current.DeepCopy()
		updated.Spec = lr.Spec
		if _, err := lrClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("update limit range %s/%s: %w", namespace, lr.Name, err)
		}
	} else if k8serrors.IsNotFound(err) {
		driftDetails = append(driftDetails, "limit range missing")
		if _, err := lrClient.Create(ctx, lr, metav1.CreateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("create limit range %s/%s: %w", namespace, lr.Name, err)
		}
	} else {
		return clusterFeedback{}, fmt.Errorf("get limit range %s/%s: %w", namespace, lr.Name, err)
	}

	cmClient := r.client.CoreV1().ConfigMaps(namespace)
	cm := policyPackManifest(namespace, class)
	if current, err := cmClient.Get(ctx, cm.Name, metav1.GetOptions{}); err == nil {
		if !reflect.DeepEqual(current.Data, cm.Data) {
			driftDetails = append(driftDetails, "policy pack config differed")
		}
		updated := current.DeepCopy()
		updated.Data = cm.Data
		updated.Labels = cm.Labels
		if _, err := cmClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("update configmap %s/%s: %w", namespace, cm.Name, err)
		}
	} else if k8serrors.IsNotFound(err) {
		driftDetails = append(driftDetails, "policy pack config missing")
		if _, err := cmClient.Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("create configmap %s/%s: %w", namespace, cm.Name, err)
		}
	} else {
		return clusterFeedback{}, fmt.Errorf("get configmap %s/%s: %w", namespace, cm.Name, err)
	}

	npClient := r.client.NetworkingV1().NetworkPolicies(namespace)
	np := networkPolicyManifest(namespace)
	if current, err := npClient.Get(ctx, np.Name, metav1.GetOptions{}); err == nil {
		if !reflect.DeepEqual(current.Spec, np.Spec) {
			driftDetails = append(driftDetails, "network policy differed")
		}
		updated := current.DeepCopy()
		updated.Spec = np.Spec
		if _, err := npClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("update network policy %s/%s: %w", namespace, np.Name, err)
		}
	} else if k8serrors.IsNotFound(err) {
		driftDetails = append(driftDetails, "network policy missing")
		if _, err := npClient.Create(ctx, np, metav1.CreateOptions{}); err != nil {
			return clusterFeedback{}, fmt.Errorf("create network policy %s/%s: %w", namespace, np.Name, err)
		}
	} else {
		return clusterFeedback{}, fmt.Errorf("get network policy %s/%s: %w", namespace, np.Name, err)
	}

	if len(driftDetails) > 0 {
		feedback.DriftStatus = "drift_corrected"
		feedback.DriftSummary = strings.Join(driftDetails, "; ")
	}

	return feedback, nil
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
				"platform.example.com/quota": req.QuotaProfile,
			},
		},
	}
}

func resourceQuotaManifest(namespace string, class domain.EnvironmentClass) *corev1.ResourceQuota {
	pods := class.DefaultNamespaces * 10
	services := class.DefaultNamespaces * 4
	pvcs := class.DefaultNamespaces * 4
	requestCPU := class.DefaultNamespaces * 4
	requestMemGi := class.DefaultNamespaces * 8
	limitCPU := class.DefaultNamespaces * 8
	limitMemGi := class.DefaultNamespaces * 16

	switch class.QuotaProfile {
	case "small":
		pods = maxInt(10, class.DefaultNamespaces*8)
		services = maxInt(4, class.DefaultNamespaces*3)
		pvcs = maxInt(4, class.DefaultNamespaces*2)
		requestCPU = maxInt(2, class.DefaultNamespaces*2)
		requestMemGi = maxInt(4, class.DefaultNamespaces*4)
		limitCPU = maxInt(4, class.DefaultNamespaces*4)
		limitMemGi = maxInt(8, class.DefaultNamespaces*8)
	case "medium":
		pods = maxInt(20, class.DefaultNamespaces*12)
		services = maxInt(6, class.DefaultNamespaces*5)
		pvcs = maxInt(6, class.DefaultNamespaces*4)
	case "large":
		pods = maxInt(40, class.DefaultNamespaces*16)
		services = maxInt(12, class.DefaultNamespaces*8)
		pvcs = maxInt(12, class.DefaultNamespaces*6)
		requestCPU = maxInt(8, class.DefaultNamespaces*6)
		requestMemGi = maxInt(16, class.DefaultNamespaces*12)
		limitCPU = maxInt(16, class.DefaultNamespaces*12)
		limitMemGi = maxInt(32, class.DefaultNamespaces*24)
	}

	hard := corev1.ResourceList{
		corev1.ResourcePods:                   resourceQuantity(strconv.Itoa(pods)),
		corev1.ResourceServices:               resourceQuantity(strconv.Itoa(services)),
		corev1.ResourcePersistentVolumeClaims: resourceQuantity(strconv.Itoa(pvcs)),
		corev1.ResourceRequestsCPU:            resourceQuantity(fmt.Sprintf("%d", requestCPU)),
		corev1.ResourceRequestsMemory:         resourceQuantity(fmt.Sprintf("%dGi", requestMemGi)),
		corev1.ResourceLimitsCPU:              resourceQuantity(fmt.Sprintf("%d", limitCPU)),
		corev1.ResourceLimitsMemory:           resourceQuantity(fmt.Sprintf("%dGi", limitMemGi)),
	}

	return &corev1.ResourceQuota{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ResourceQuota",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "platform-defaults",
			Namespace: namespace,
			Labels: map[string]string{
				"platform.example.com/quota-profile": class.QuotaProfile,
			},
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: hard,
		},
	}
}

func limitRangeManifest(namespace string, class domain.EnvironmentClass) *corev1.LimitRange {
	defaultCPU := "500m"
	defaultMemory := "512Mi"
	maxCPU := "2"
	maxMemory := "2Gi"

	switch class.QuotaProfile {
	case "medium":
		defaultCPU = "1"
		defaultMemory = "1Gi"
		maxCPU = "4"
		maxMemory = "8Gi"
	case "large":
		defaultCPU = "2"
		defaultMemory = "2Gi"
		maxCPU = "8"
		maxMemory = "16Gi"
	}

	return &corev1.LimitRange{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "LimitRange",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "platform-defaults",
			Namespace: namespace,
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{
				{
					Type: corev1.LimitTypeContainer,
					Default: corev1.ResourceList{
						corev1.ResourceCPU:    resourceQuantity(defaultCPU),
						corev1.ResourceMemory: resourceQuantity(defaultMemory),
					},
					DefaultRequest: corev1.ResourceList{
						corev1.ResourceCPU:    resourceQuantity(defaultCPU),
						corev1.ResourceMemory: resourceQuantity(defaultMemory),
					},
					Max: corev1.ResourceList{
						corev1.ResourceCPU:    resourceQuantity(maxCPU),
						corev1.ResourceMemory: resourceQuantity(maxMemory),
					},
				},
			},
		},
	}
}

func policyPackManifest(namespace string, class domain.EnvironmentClass) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "platform-policy-pack",
			Namespace: namespace,
			Labels: map[string]string{
				"platform.example.com/quota-profile": class.QuotaProfile,
			},
		},
		Data: map[string]string{
			"policy-packs":               strings.Join(class.PolicyPacks, "\n"),
			"quota-profile":              class.QuotaProfile,
			"estimated-monthly-cost-usd": strconv.Itoa(class.EstimatedMonthlyCostUSD),
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
			"annotations": map[string]string{
				"platform.example.com/source-revision": req.Revision,
			},
			"labels": map[string]string{
				"platform.example.com/team":          req.Team,
				"platform.example.com/quota-profile": req.QuotaProfile,
			},
		},
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{
				"repoURL":        repoURL,
				"path":           relativePath,
				"targetRevision": fallbackBranch(cfg.GitBaseBranch, cfg.GitBranch),
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

func normalizeLabelSubset(current, desired map[string]string) map[string]string {
	out := make(map[string]string, len(desired))
	for key := range desired {
		out[key] = current[key]
	}
	return out
}

func sanitizeBranchSegment(in string) string {
	in = strings.TrimSpace(strings.ToLower(in))
	in = strings.ReplaceAll(in, "_", "-")
	in = strings.ReplaceAll(in, " ", "-")
	re := regexp.MustCompile(`[^a-z0-9._/-]+`)
	in = re.ReplaceAllString(in, "-")
	in = strings.Trim(in, "-/.")
	if in == "" {
		return "promotion"
	}
	return in
}

func inferGitHubRepo(remote string) string {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, ".git")
	remote = strings.TrimPrefix(remote, "https://github.com/")
	remote = strings.TrimPrefix(remote, "git@github.com:")
	if strings.Count(remote, "/") >= 1 {
		return remote
	}
	return ""
}

func fallbackBranch(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return strings.TrimSpace(primary)
	}
	return strings.TrimSpace(fallback)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
