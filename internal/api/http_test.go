package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mcmoney/platform-control-plane/internal/auth"
	"github.com/mcmoney/platform-control-plane/internal/domain"
	"github.com/mcmoney/platform-control-plane/internal/reconcile"
	"github.com/mcmoney/platform-control-plane/internal/service"
	"github.com/mcmoney/platform-control-plane/internal/store"
)

func TestCreateAndFetchRequest(t *testing.T) {
	repo := store.NewMemoryRepository(service.DefaultClasses())
	svc := service.New(slog.Default(), repo, fakeAPITestReconciler{}, noop.NewMeterProvider().Meter("test"), service.Config{})
	authenticator, err := auth.NewAuthenticator(context.Background(), auth.Config{
		Enabled:         true,
		StaticJWTSecret: "test-secret",
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	server := NewServer(slog.Default(), authenticator, svc, time.Second, NamedChecker("repository", repo), NamedChecker("auth", authenticator))

	body := domain.CreateRequestInput{
		App:        "payments-api",
		Team:       "platform",
		Class:      "preview",
		Region:     "us-east-1",
		TTLHours:   24,
		Owner:      "mcmoney",
		Repository: "https://github.com/mcmoney/payments-api",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/v1/environment-requests", bytes.NewReader(payload))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+mintTestToken(t, "test-secret", "requester1", "requester1@example.com", string(auth.RoleRequester)))
	createRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRes, createReq)

	if createRes.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, createRes.Code)
	}

	var created domain.EnvironmentRequest
	if err := json.NewDecoder(createRes.Body).Decode(&created); err != nil {
		t.Fatalf("Decode create response: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/environment-requests/"+created.ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+mintTestToken(t, "test-secret", "viewer1", "viewer1@example.com", string(auth.RoleViewer)))
	getRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRes, getReq)

	if getRes.Code != http.StatusOK {
		body, _ := io.ReadAll(getRes.Body)
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, getRes.Code, string(body))
	}
}

func TestRejectsMissingAuthHeaders(t *testing.T) {
	repo := store.NewMemoryRepository(service.DefaultClasses())
	svc := service.New(slog.Default(), repo, fakeAPITestReconciler{}, noop.NewMeterProvider().Meter("test"), service.Config{})
	authenticator, err := auth.NewAuthenticator(context.Background(), auth.Config{
		Enabled:         true,
		StaticJWTSecret: "test-secret",
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	server := NewServer(slog.Default(), authenticator, svc, time.Second, NamedChecker("repository", repo), NamedChecker("auth", authenticator))

	req := httptest.NewRequest(http.MethodGet, "/v1/environment-classes", nil)
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
}

type fakeAPITestReconciler struct{}

func (fakeAPITestReconciler) Reconcile(context.Context, domain.EnvironmentRequest, domain.EnvironmentClass) (reconcile.Result, error) {
	return reconcile.Result{}, nil
}

func (fakeAPITestReconciler) Ready(context.Context) error {
	return nil
}

func mintTestToken(t *testing.T, secret, subject, actor, role string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   subject,
		"email": actor,
		"role":  role,
		"exp":   253402300799,
	})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	return signed
}
