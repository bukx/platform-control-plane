package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/mcmoney/platform-control-plane/internal/auth"
	"github.com/mcmoney/platform-control-plane/internal/domain"
	"github.com/mcmoney/platform-control-plane/internal/reconcile"
	"github.com/mcmoney/platform-control-plane/internal/store"
)

type fakeReconciler struct {
	result reconcile.Result
	err    error
}

func (f fakeReconciler) Reconcile(context.Context, domain.EnvironmentRequest, domain.EnvironmentClass) (reconcile.Result, error) {
	return f.result, f.err
}

func (f fakeReconciler) Ready(context.Context) error {
	return nil
}

type fakeQueue struct {
	requestIDs []string
	err        error
}

func (f *fakeQueue) Enqueue(_ context.Context, requestID string) error {
	if f.err != nil {
		return f.err
	}
	f.requestIDs = append(f.requestIDs, requestID)
	return nil
}

func approverContext() context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Actor:             "approver1",
		Role:              auth.RoleApprover,
		ApprovalSignature: auth.ComputeApprovalSignature("test-secret", "placeholder", "approver1", "production"),
	})
}

func TestCreatePreviewRequestStartsApproved(t *testing.T) {
	repo := store.NewMemoryRepository(DefaultClasses())
	svc := New(slog.Default(), repo, fakeReconciler{}, noop.NewMeterProvider().Meter("test"), Config{})
	svc.clock = func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }

	req, err := svc.CreateRequest(context.Background(), domain.CreateRequestInput{
		App:        "payments-api",
		Team:       "platform",
		Class:      "preview",
		Region:     "us-east-1",
		TTLHours:   24,
		Owner:      "mcmoney",
		Repository: "https://github.com/mcmoney/payments-api",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}

	if req.Status != domain.StatusApproved {
		t.Fatalf("expected approved status, got %s", req.Status)
	}
}

func TestCreateProductionRequestNeedsApproval(t *testing.T) {
	repo := store.NewMemoryRepository(DefaultClasses())
	svc := New(slog.Default(), repo, fakeReconciler{}, noop.NewMeterProvider().Meter("test"), Config{})

	req, err := svc.CreateRequest(context.Background(), domain.CreateRequestInput{
		App:        "billing-api",
		Team:       "platform",
		Class:      "production",
		Region:     "us-east-1",
		TTLHours:   12,
		Owner:      "mcmoney",
		Repository: "https://github.com/mcmoney/billing-api",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}

	if req.Status != domain.StatusPendingApproval {
		t.Fatalf("expected pending approval status, got %s", req.Status)
	}
}

func TestCreateRequestRejectsInvalidTTL(t *testing.T) {
	repo := store.NewMemoryRepository(DefaultClasses())
	svc := New(slog.Default(), repo, fakeReconciler{}, noop.NewMeterProvider().Meter("test"), Config{})

	_, err := svc.CreateRequest(context.Background(), domain.CreateRequestInput{
		App:        "billing-api",
		Team:       "platform",
		Class:      "production",
		Region:     "us-east-1",
		TTLHours:   999,
		Owner:      "mcmoney",
		Repository: "https://github.com/mcmoney/billing-api",
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestApproveEnqueueThenProcess(t *testing.T) {
	repo := store.NewMemoryRepository(DefaultClasses())
	queue := &fakeQueue{}
	svc := New(slog.Default(), repo, fakeReconciler{
		result: reconcile.Result{
			Namespace:    "platform-billing-api-1234abcd",
			GitOpsPath:   "/tmp/gitops/clusters/us-east-1/teams/platform/billing-api/req-1234abcd",
			GitCommitSHA: "abc123",
			GitBranch:    "main",
		},
	}, noop.NewMeterProvider().Meter("test"), Config{
		ApprovalHMACSecret: "test-secret",
	})
	svc.SetQueue(queue)
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	svc.clock = func() time.Time { return now }

	req, err := svc.CreateRequest(context.Background(), domain.CreateRequestInput{
		App:        "billing-api",
		Team:       "platform",
		Class:      "production",
		Region:     "us-east-1",
		TTLHours:   12,
		Owner:      "mcmoney",
		Repository: "https://github.com/mcmoney/billing-api",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}

	approveCtx := auth.WithIdentity(context.Background(), auth.Identity{
		Actor:             "approver1",
		Role:              auth.RoleApprover,
		ApprovalSignature: auth.ComputeApprovalSignature("test-secret", req.ID, "approver1", "production"),
	})
	req, err = svc.ApproveRequest(approveCtx, req.ID)
	if err != nil {
		t.Fatalf("ApproveRequest returned error: %v", err)
	}
	if req.Status != domain.StatusApproved {
		t.Fatalf("expected approved status after approval, got %s", req.Status)
	}

	req, err = svc.ReconcileRequest(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("ReconcileRequest returned error: %v", err)
	}
	if req.Status != domain.StatusQueued {
		t.Fatalf("expected queued status after enqueue, got %s", req.Status)
	}
	if len(queue.requestIDs) != 1 || queue.requestIDs[0] != req.ID {
		t.Fatalf("expected request to be enqueued, got %#v", queue.requestIDs)
	}

	if err := svc.ProcessReconcileRequest(context.Background(), req.ID); err != nil {
		t.Fatalf("ProcessReconcileRequest returned error: %v", err)
	}

	req, err = svc.GetRequest(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("GetRequest returned error: %v", err)
	}
	if req.Status != domain.StatusReady {
		t.Fatalf("expected ready status after processing, got %s", req.Status)
	}
	if req.LastReconciledAt == nil {
		t.Fatal("expected LastReconciledAt to be set")
	}
	if req.Namespace == "" {
		t.Fatal("expected Namespace to be set")
	}
	if req.GitOpsPath == "" {
		t.Fatal("expected GitOpsPath to be set")
	}
	if req.GitCommitSHA == "" {
		t.Fatal("expected GitCommitSHA to be set")
	}
}
