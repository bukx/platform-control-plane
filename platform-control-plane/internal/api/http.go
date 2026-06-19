package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mcmoney/platform-control-plane/internal/auth"
	"github.com/mcmoney/platform-control-plane/internal/domain"
	"github.com/mcmoney/platform-control-plane/internal/service"
	"github.com/mcmoney/platform-control-plane/internal/store"
)

type PlatformService interface {
	ListClasses(context.Context) ([]domain.EnvironmentClass, error)
	ListRequests(context.Context) ([]domain.EnvironmentRequest, error)
	GetRequest(context.Context, string) (domain.EnvironmentRequest, error)
	CreateRequest(context.Context, domain.CreateRequestInput) (domain.EnvironmentRequest, error)
	ApproveRequest(context.Context, string) (domain.EnvironmentRequest, error)
	ReconcileRequest(context.Context, string) (domain.EnvironmentRequest, error)
}

type Server struct {
	logger        *slog.Logger
	authenticator auth.Authenticator
	service       PlatformService
	mux           *http.ServeMux
	readiness     []namedChecker
	readyTimeout  time.Duration
}

type ReadinessChecker interface {
	Ready(context.Context) error
}

type namedChecker struct {
	name    string
	checker ReadinessChecker
}

func NewServer(logger *slog.Logger, authenticator auth.Authenticator, svc PlatformService, readyTimeout time.Duration, readiness ...namedChecker) *Server {
	if readyTimeout <= 0 {
		readyTimeout = 3 * time.Second
	}
	s := &Server{
		logger:        logger,
		authenticator: authenticator,
		service:       svc,
		mux:           http.NewServeMux(),
		readiness:     readiness,
		readyTimeout:  readyTimeout,
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.loggingMiddleware(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleReady)
	s.mux.HandleFunc("GET /metrics", s.handleNotFound)
	s.mux.Handle("GET /v1/environment-classes", s.withIdentity([]auth.Role{auth.RoleViewer, auth.RoleRequester, auth.RoleApprover, auth.RoleAdmin}, http.HandlerFunc(s.handleListClasses)))
	s.mux.Handle("GET /v1/environment-requests", s.withIdentity([]auth.Role{auth.RoleViewer, auth.RoleRequester, auth.RoleApprover, auth.RoleAdmin}, http.HandlerFunc(s.handleListRequests)))
	s.mux.Handle("POST /v1/environment-requests", s.withIdentity([]auth.Role{auth.RoleRequester, auth.RoleAdmin}, http.HandlerFunc(s.handleCreateRequest)))
	s.mux.Handle("GET /v1/environment-requests/{id}", s.withIdentity([]auth.Role{auth.RoleViewer, auth.RoleRequester, auth.RoleApprover, auth.RoleAdmin}, http.HandlerFunc(s.handleGetRequest)))
	s.mux.Handle("POST /v1/environment-requests/{id}/approve", s.withIdentity([]auth.Role{auth.RoleApprover, auth.RoleAdmin}, http.HandlerFunc(s.handleApproveRequest)))
	s.mux.Handle("POST /v1/environment-requests/{id}/reconcile", s.withIdentity([]auth.Role{auth.RoleApprover, auth.RoleAdmin}, http.HandlerFunc(s.handleReconcileRequest)))
}

func (s *Server) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, errors.New("endpoint not configured"))
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), s.readyTimeout)
	defer cancel()

	failures := map[string]string{}
	for _, check := range s.readiness {
		if check.checker == nil {
			continue
		}
		if err := check.checker.Ready(ctx); err != nil {
			failures[check.name] = err.Error()
		}
	}
	if len(failures) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "not_ready",
			"failures": failures,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleListClasses(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.ListClasses(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.ListRequests(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	var in domain.CreateRequestInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	req, err := s.service.CreateRequest(r.Context(), in)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, req)
}

func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	req, err := s.service.GetRequest(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, req)
}

func (s *Server) handleApproveRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	req, err := s.service.ApproveRequest(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, req)
}

func (s *Server) handleReconcileRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	req, err := s.service.ReconcileRequest(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, req)
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)
		s.logger.Info("http_request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", ww.status),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

func (s *Server) withIdentity(allowed []auth.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authenticator == nil {
			identity := auth.Identity{Actor: "system", Role: auth.RoleAdmin}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
			return
		}

		identity, err := s.authenticator.Authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		if !auth.RoleAllowed(identity.Role, allowed...) {
			writeError(w, http.StatusForbidden, errors.New("role not allowed for this action"))
			return
		}

		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, service.ErrValidation):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, service.ErrConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, service.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, service.ErrQueueUnavailable):
		writeError(w, http.StatusServiceUnavailable, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{
		"error": strings.TrimSpace(err.Error()),
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func NamedChecker(name string, checker ReadinessChecker) namedChecker {
	return namedChecker{
		name:    strings.TrimSpace(fmt.Sprintf("%s", name)),
		checker: checker,
	}
}
