package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/mcmoney/platform-control-plane/internal/auth"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/mcmoney/platform-control-plane/internal/domain"
	"github.com/mcmoney/platform-control-plane/internal/reconcile"
	"github.com/mcmoney/platform-control-plane/internal/store"
)

var (
	ErrValidation       = errors.New("validation failed")
	ErrConflict         = errors.New("invalid state transition")
	ErrUnauthorized     = errors.New("unauthorized")
	ErrQueueUnavailable = errors.New("reconcile queue unavailable")
)

type Config struct {
	ApprovalHMACSecret string
}

type ReconcileQueue interface {
	Enqueue(context.Context, string) error
}

type PlatformService struct {
	logger             *slog.Logger
	repo               store.Repository
	reconciler         reconcile.Reconciler
	queue              ReconcileQueue
	approvalSecret     string
	tracer             trace.Tracer
	requestCreateCount metric.Int64Counter
	reconcileCount     metric.Int64Counter
	clock              func() time.Time
}

func New(logger *slog.Logger, repo store.Repository, reconciler reconcile.Reconciler, meter metric.Meter, cfg Config) *PlatformService {
	requestCreateCount, _ := meter.Int64Counter("platform.environment_requests.created")
	reconcileCount, _ := meter.Int64Counter("platform.environment_requests.reconciled")

	return &PlatformService{
		logger:             logger,
		repo:               repo,
		reconciler:         reconciler,
		approvalSecret:     cfg.ApprovalHMACSecret,
		tracer:             otel.Tracer("platform-control-plane/service"),
		requestCreateCount: requestCreateCount,
		reconcileCount:     reconcileCount,
		clock: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *PlatformService) SetQueue(queue ReconcileQueue) {
	s.queue = queue
}

func DefaultClasses() []domain.EnvironmentClass {
	return []domain.EnvironmentClass{
		{
			Name:                    "preview",
			Description:             "Self-service preview environments for pull requests and fast validation.",
			AllowedRegions:          []string{"us-east-1", "us-west-2"},
			RequiresApproval:        false,
			MaxTTLHours:             72,
			DefaultNamespaces:       1,
			QuotaProfile:            "small",
			PolicyPacks:             []string{"baseline-security", "preview-networking", "cost-guardrails"},
			EstimatedMonthlyCostUSD: 150,
		},
		{
			Name:                    "production",
			Description:             "Approval-gated production environments with stricter policy boundaries.",
			AllowedRegions:          []string{"us-east-1", "us-west-2"},
			RequiresApproval:        true,
			MaxTTLHours:             24,
			DefaultNamespaces:       4,
			QuotaProfile:            "large",
			PolicyPacks:             []string{"baseline-security", "zero-trust-networking", "audit-retention", "sox-controls"},
			EstimatedMonthlyCostUSD: 2400,
		},
		{
			Name:                    "shared-staging",
			Description:             "Shared staging space for multi-service integration and manual QA.",
			AllowedRegions:          []string{"us-east-1"},
			RequiresApproval:        false,
			MaxTTLHours:             168,
			DefaultNamespaces:       2,
			QuotaProfile:            "medium",
			PolicyPacks:             []string{"baseline-security", "shared-services", "cost-guardrails"},
			EstimatedMonthlyCostUSD: 700,
		},
	}
}

func (s *PlatformService) ListClasses(ctx context.Context) ([]domain.EnvironmentClass, error) {
	return s.repo.ListClasses(ctx)
}

func (s *PlatformService) ListRequests(ctx context.Context) ([]domain.EnvironmentRequest, error) {
	return s.repo.ListRequests(ctx)
}

func (s *PlatformService) GetRequest(ctx context.Context, id string) (domain.EnvironmentRequest, error) {
	return s.repo.GetRequest(ctx, id)
}

func (s *PlatformService) CreateRequest(ctx context.Context, in domain.CreateRequestInput) (domain.EnvironmentRequest, error) {
	ctx, span := s.tracer.Start(ctx, "service.CreateRequest")
	defer span.End()

	class, err := s.repo.GetClass(ctx, in.Class)
	if err != nil {
		return domain.EnvironmentRequest{}, fmt.Errorf("%w: unknown environment class %q", ErrValidation, in.Class)
	}

	if err := validateInput(in, class); err != nil {
		return domain.EnvironmentRequest{}, err
	}

	status := domain.StatusApproved
	if class.RequiresApproval {
		status = domain.StatusPendingApproval
	}

	req := domain.EnvironmentRequest{
		ID:                      newID(),
		App:                     strings.TrimSpace(in.App),
		Team:                    strings.TrimSpace(in.Team),
		Class:                   class.Name,
		Region:                  strings.TrimSpace(in.Region),
		TTLHours:                in.TTLHours,
		Owner:                   strings.TrimSpace(in.Owner),
		Repository:              strings.TrimSpace(in.Repository),
		Revision:                defaultRevision(in.Revision),
		Labels:                  sanitizeLabels(in.Labels),
		Status:                  status,
		CreatedAt:               s.clock(),
		QuotaProfile:            class.QuotaProfile,
		PolicyPacks:             append([]string(nil), class.PolicyPacks...),
		EstimatedMonthlyCostUSD: class.EstimatedMonthlyCostUSD,
	}

	created, err := s.repo.CreateRequest(ctx, req)
	if err != nil {
		return domain.EnvironmentRequest{}, err
	}

	s.requestCreateCount.Add(ctx, 1, metric.WithAttributes(
		attribute.String("class", created.Class),
		attribute.String("team", created.Team),
	))
	span.SetAttributes(
		attribute.String("request.id", created.ID),
		attribute.String("request.class", created.Class),
	)
	return created, nil
}

func (s *PlatformService) ApproveRequest(ctx context.Context, id string) (domain.EnvironmentRequest, error) {
	ctx, span := s.tracer.Start(ctx, "service.ApproveRequest")
	defer span.End()

	identity, ok := auth.FromContext(ctx)
	if !ok {
		return domain.EnvironmentRequest{}, fmt.Errorf("%w: missing identity context", ErrUnauthorized)
	}

	req, err := s.repo.GetRequest(ctx, id)
	if err != nil {
		return domain.EnvironmentRequest{}, err
	}

	if req.Status != domain.StatusPendingApproval {
		return domain.EnvironmentRequest{}, fmt.Errorf("%w: request %s is not awaiting approval", ErrConflict, id)
	}
	if !auth.VerifyApprovalSignature(s.approvalSecret, req.ID, identity.Actor, req.Class, identity.ApprovalSignature) {
		return domain.EnvironmentRequest{}, fmt.Errorf("%w: invalid approval signature", ErrUnauthorized)
	}

	now := s.clock()
	req.Status = domain.StatusApproved
	req.ApprovedAt = &now
	req.ApprovedBy = identity.Actor
	req.ApprovalSignature = identity.ApprovalSignature
	req.LastError = ""

	return s.repo.UpdateRequest(ctx, req)
}

func (s *PlatformService) ReconcileRequest(ctx context.Context, id string) (domain.EnvironmentRequest, error) {
	ctx, span := s.tracer.Start(ctx, "service.EnqueueReconcileRequest")
	defer span.End()

	if s.queue == nil {
		return domain.EnvironmentRequest{}, ErrQueueUnavailable
	}

	req, err := s.repo.GetRequest(ctx, id)
	if err != nil {
		return domain.EnvironmentRequest{}, err
	}

	if req.Status == domain.StatusPendingApproval {
		return domain.EnvironmentRequest{}, fmt.Errorf("%w: request %s still needs approval", ErrConflict, id)
	}
	if req.Status == domain.StatusQueued || req.Status == domain.StatusReconciling {
		return domain.EnvironmentRequest{}, fmt.Errorf("%w: request %s is already queued for reconciliation", ErrConflict, id)
	}

	now := s.clock()
	if err := s.queue.Enqueue(ctx, id); err != nil {
		return domain.EnvironmentRequest{}, err
	}

	req.Status = domain.StatusQueued
	req.QueuedAt = &now
	req.LastError = ""

	updated, err := s.repo.UpdateRequest(ctx, req)
	if err != nil {
		return domain.EnvironmentRequest{}, err
	}

	return updated, nil
}

func (s *PlatformService) ProcessReconcileRequest(ctx context.Context, id string) error {
	ctx, span := s.tracer.Start(ctx, "service.ProcessReconcileRequest")
	defer span.End()

	req, err := s.repo.GetRequest(ctx, id)
	if err != nil {
		return err
	}
	class, err := s.repo.GetClass(ctx, req.Class)
	if err != nil {
		return err
	}

	now := s.clock()
	req.Status = domain.StatusReconciling
	req.LastReconciledAt = &now
	req.LastError = ""

	req, err = s.repo.UpdateRequest(ctx, req)
	if err != nil {
		return err
	}

	result, err := s.reconciler.Reconcile(ctx, req, class)
	if err != nil {
		req.Status = domain.StatusFailed
		req.LastError = err.Error()
		req.QueuedAt = nil
		_, updateErr := s.repo.UpdateRequest(ctx, req)
		if updateErr != nil {
			return errors.Join(err, updateErr)
		}
		return err
	}

	req.Status = domain.StatusReady
	req.QueuedAt = nil
	req.Namespace = result.Namespace
	req.GitOpsPath = result.GitOpsPath
	req.GitCommitSHA = result.GitCommitSHA
	req.GitBranch = result.GitBranch
	req.GitPromotionMode = result.GitPromotionMode
	req.GitPromotionBranch = result.GitPromotionBranch
	req.GitPromotionURL = result.GitPromotionURL
	req.ClusterStatus = result.ClusterStatus
	req.DriftStatus = result.DriftStatus
	req.DriftSummary = result.DriftSummary
	req.QuotaProfile = class.QuotaProfile
	req.PolicyPacks = append([]string(nil), class.PolicyPacks...)
	req.EstimatedMonthlyCostUSD = class.EstimatedMonthlyCostUSD
	req.LastError = ""
	updated, err := s.repo.UpdateRequest(ctx, req)
	if err != nil {
		return err
	}

	s.reconcileCount.Add(ctx, 1, metric.WithAttributes(
		attribute.String("class", req.Class),
		attribute.String("team", req.Team),
		attribute.String("region", req.Region),
	))
	span.SetAttributes(
		attribute.String("request.id", req.ID),
		attribute.String("kubernetes.namespace", req.Namespace),
		attribute.String("gitops.path", req.GitOpsPath),
	)
	s.logger.Info("request_ready", "id", req.ID, "namespace", req.Namespace, "gitops_path", req.GitOpsPath)

	_ = updated
	return nil
}

func (s *PlatformService) ReplayQueuedRequests(ctx context.Context) (int, error) {
	if s.queue == nil {
		return 0, ErrQueueUnavailable
	}

	requests, err := s.repo.ListRequests(ctx)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, req := range requests {
		if req.Status != domain.StatusQueued && req.Status != domain.StatusReconciling {
			continue
		}
		if err := s.queue.Enqueue(ctx, req.ID); err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

func validateInput(in domain.CreateRequestInput, class domain.EnvironmentClass) error {
	if strings.TrimSpace(in.App) == "" {
		return fmt.Errorf("%w: app is required", ErrValidation)
	}
	if strings.TrimSpace(in.Team) == "" {
		return fmt.Errorf("%w: team is required", ErrValidation)
	}
	if strings.TrimSpace(in.Owner) == "" {
		return fmt.Errorf("%w: owner is required", ErrValidation)
	}
	if strings.TrimSpace(in.Repository) == "" {
		return fmt.Errorf("%w: repository is required", ErrValidation)
	}
	if strings.TrimSpace(in.Region) == "" {
		return fmt.Errorf("%w: region is required", ErrValidation)
	}
	if in.TTLHours <= 0 {
		return fmt.Errorf("%w: ttl_hours must be greater than zero", ErrValidation)
	}
	if in.TTLHours > class.MaxTTLHours {
		return fmt.Errorf("%w: ttl_hours %d exceeds class max of %d", ErrValidation, in.TTLHours, class.MaxTTLHours)
	}
	if !slices.Contains(class.AllowedRegions, in.Region) {
		return fmt.Errorf("%w: region %q is not allowed for class %q", ErrValidation, in.Region, class.Name)
	}

	return nil
}

func defaultRevision(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return "main"
	}
	return in
}

func sanitizeLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}

	return out
}

func newID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		now := time.Now().UTC().UnixNano()
		return fmt.Sprintf("req-%d", now)
	}

	return "req-" + hex.EncodeToString(buf[:])
}
