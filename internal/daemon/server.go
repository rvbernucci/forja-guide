// Package daemon exposes the kernel HTTP control-plane boundary.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/runstate"
	"github.com/rvbernucci/forja-guide/internal/version"
)

const maxRequestBody = 1 << 20

// IDGenerator creates run identifiers at the external command boundary.
type IDGenerator func() (identity.RunID, error)

// Server is the storage-neutral kernel daemon.
type Server struct {
	store         runstate.Repository
	readiness     readinessProbe
	registry      *contracts.Registry
	newID         IDGenerator
	logger        *slog.Logger
	authenticator Authenticator
	authority     control.Authority
	telemetry     *observability.Runtime
	ready         atomic.Bool
}

type readinessProbe interface {
	Ready(context.Context) error
}

type principalContextKey struct{}

// New creates a daemon with explicit dependencies.
func New(
	store runstate.Repository,
	registry *contracts.Registry,
	authenticator Authenticator,
	authority control.Authority,
	newID IDGenerator,
	logger *slog.Logger,
	telemetry ...*observability.Runtime,
) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if registry == nil {
		return nil, fmt.Errorf("contract registry is required")
	}
	if authenticator == nil {
		return nil, fmt.Errorf("HTTP authenticator is required")
	}
	if strings.TrimSpace(authority.TenantID) == "" ||
		strings.TrimSpace(authority.RepositoryID) == "" {
		return nil, fmt.Errorf("HTTP authority is required")
	}
	if newID == nil {
		newID = identity.NewRunID
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if len(telemetry) > 1 {
		return nil, fmt.Errorf("daemon accepts at most one telemetry runtime")
	}
	server := &Server{
		store:         store,
		registry:      registry,
		authenticator: authenticator,
		authority:     authority,
		newID:         newID,
		logger:        logger,
	}
	if len(telemetry) == 1 {
		server.telemetry = telemetry[0]
	}
	if probe, ok := store.(readinessProbe); ok {
		server.readiness = probe
	}
	server.ready.Store(true)
	return server, nil
}

// Handler returns the complete HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /version", s.handleVersion)
	if s.telemetry != nil {
		mux.Handle("GET /metrics", s.telemetry.MetricsHandler())
	}
	mux.Handle("POST /v1/runs", s.protect(control.PermissionLegacyRunWrite, s.handleCreateRun))
	mux.Handle("GET /v1/runs/{run_id}", s.protect(control.PermissionRead, s.handleGetRun))
	mux.Handle("POST /v1/runs/{run_id}/transitions", s.protect(control.PermissionLegacyRunWrite, s.handleTransitionRun))
	handler := requestLogMiddleware(s.logger, s.authenticateV1(mux))
	if s.telemetry != nil && s.telemetry.Observer != nil {
		handler = s.telemetry.Observer.HTTPHandler(handler)
	}
	return handler
}

// SetReady changes readiness without changing liveness.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(writer http.ResponseWriter, request *http.Request) {
	if !s.ready.Load() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready",
		})
		return
	}
	if s.readiness != nil {
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if err := s.readiness.Ready(ctx); err != nil {
			s.logger.WarnContext(
				request.Context(),
				"readiness dependency unavailable",
				"failure_class",
				observability.Classify(err),
			)
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{
				"status": "not_ready",
			})
			return
		}
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleVersion(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, version.Current())
}

type createRunRequest struct {
	Objective string `json:"objective"`
}

type protectedHandler func(http.ResponseWriter, *http.Request, control.Principal)

func (s *Server) authenticateV1(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1" && !strings.HasPrefix(request.URL.Path, "/v1/") {
			next.ServeHTTP(writer, request)
			return
		}
		principal, err := s.authenticator.Authenticate(request)
		if err != nil {
			s.writeError(request.Context(), writer, err)
			return
		}
		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func (s *Server) protect(permission control.Permission, next protectedHandler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, ok := request.Context().Value(principalContextKey{}).(control.Principal)
		if !ok {
			s.writeError(request.Context(), writer, unauthenticated())
			return
		}
		if err := s.authorize(principal, permission); err != nil {
			s.writeError(request.Context(), writer, err)
			return
		}
		next(writer, request, principal)
	})
}

func (s *Server) authorize(principal control.Principal, permission control.Permission) error {
	if principal.ActorType == "" || principal.ActorID == "" {
		return unauthenticated()
	}
	if _, ok := principal.Permissions[permission]; !ok {
		return fault.New(
			fault.CodePermissionDenied,
			"daemon.authorize",
			fmt.Sprintf("permission %q is required", permission),
		)
	}
	if principal.TenantID != s.authority.TenantID ||
		principal.RepositoryID != s.authority.RepositoryID {
		return fault.New(
			fault.CodePermissionDenied,
			"daemon.authorize",
			"principal authority does not match the bound repository",
		)
	}
	return nil
}

func (s *Server) handleCreateRun(writer http.ResponseWriter, request *http.Request, principal control.Principal) {
	var command createRunRequest
	if err := decodeRequest(writer, request, &command); err != nil {
		s.writeError(request.Context(), writer, err)
		return
	}
	id, err := s.newID()
	if err != nil {
		s.writeError(request.Context(), writer, fault.Wrap(
			fault.CodeInternal,
			"daemon.createRun",
			"generate run identifier",
			err,
		))
		return
	}
	metadata := commandMetadata(request, id.String(), principal)
	run, err := s.store.CreateRun(
		request.Context(),
		id,
		command.Objective,
		metadata,
	)
	if err != nil {
		s.writeError(request.Context(), writer, err)
		return
	}
	if err := s.validateRun(run); err != nil {
		s.writeError(request.Context(), writer, err)
		return
	}
	writer.Header().Set("Idempotency-Key", metadata.IdempotencyKey)
	writeJSON(writer, http.StatusCreated, run)
}

func (s *Server) handleGetRun(writer http.ResponseWriter, request *http.Request, _ control.Principal) {
	id, err := identity.ParseRunID(request.PathValue("run_id"))
	if err != nil {
		s.writeError(request.Context(), writer, fault.Wrap(
			fault.CodeInvalidArgument,
			"daemon.getRun",
			"invalid run identifier",
			err,
		))
		return
	}
	run, err := s.store.GetRun(request.Context(), id)
	if err != nil {
		s.writeError(request.Context(), writer, err)
		return
	}
	if err := s.validateRun(run); err != nil {
		s.writeError(request.Context(), writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, run)
}

type transitionRunRequest struct {
	ExpectedVersion int    `json:"expected_version"`
	TargetState     string `json:"target_state"`
}

func (s *Server) handleTransitionRun(writer http.ResponseWriter, request *http.Request, principal control.Principal) {
	id, err := identity.ParseRunID(request.PathValue("run_id"))
	if err != nil {
		s.writeError(request.Context(), writer, fault.Wrap(
			fault.CodeInvalidArgument,
			"daemon.transitionRun",
			"invalid run identifier",
			err,
		))
		return
	}
	var command transitionRunRequest
	if err := decodeRequest(writer, request, &command); err != nil {
		s.writeError(request.Context(), writer, err)
		return
	}
	if command.ExpectedVersion < 1 {
		s.writeError(request.Context(), writer, fault.New(
			fault.CodeInvalidArgument,
			"daemon.transitionRun",
			"expected_version must be at least 1",
		))
		return
	}
	target, err := runstate.ParseState(command.TargetState)
	if err != nil {
		s.writeError(request.Context(), writer, err)
		return
	}
	requestID, err := s.newID()
	if err != nil {
		s.writeError(request.Context(), writer, fault.Wrap(
			fault.CodeInternal,
			"daemon.transitionRun",
			"generate command identifier",
			err,
		))
		return
	}
	metadata := commandMetadata(request, requestID.String(), principal)
	run, err := s.store.TransitionRun(
		request.Context(),
		id,
		command.ExpectedVersion,
		target,
		metadata,
	)
	if err != nil {
		s.writeError(request.Context(), writer, err)
		return
	}
	if err := s.validateRun(run); err != nil {
		s.writeError(request.Context(), writer, err)
		return
	}
	writer.Header().Set("Idempotency-Key", metadata.IdempotencyKey)
	writeJSON(writer, http.StatusOK, run)
}

func commandMetadata(
	request *http.Request,
	fallback string,
	principal control.Principal,
) runstate.CommandMetadata {
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		key = "generated:" + fallback
	}
	correlationID := strings.TrimSpace(request.Header.Get("Forja-Correlation-ID"))
	if correlationID == "" {
		correlationID = fallback
	}
	var causationID *string
	if value := strings.TrimSpace(request.Header.Get("Forja-Causation-ID")); value != "" {
		causationID = &value
	}
	return runstate.CommandMetadata{
		IdempotencyKey: key,
		ActorType:      principal.ActorType,
		ActorID:        principal.ActorID,
		CorrelationID:  correlationID,
		CausationID:    causationID,
	}
}

func (s *Server) validateRun(run contracts.Run) error {
	data, err := json.Marshal(run)
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "daemon.validateRun", "encode run", err)
	}
	if err := s.registry.ValidateJSON("run.schema.json", data); err != nil {
		return fault.Wrap(
			fault.CodeInternal,
			"daemon.validateRun",
			"run violated canonical contract",
			err,
		)
	}
	return nil
}

type errorResponse struct {
	Error struct {
		Code    fault.Code `json:"code"`
		Message string     `json:"message"`
	} `json:"error"`
}

func (s *Server) writeError(ctx context.Context, writer http.ResponseWriter, err error) {
	code := fault.CodeOf(err)
	status := http.StatusInternalServerError
	switch code {
	case fault.CodeInvalidArgument:
		status = http.StatusBadRequest
	case fault.CodeUnauthenticated:
		status = http.StatusUnauthorized
		writer.Header().Set("WWW-Authenticate", `Bearer realm="forjad"`)
	case fault.CodePermissionDenied:
		status = http.StatusForbidden
	case fault.CodeNotFound:
		status = http.StatusNotFound
	case fault.CodeConflict:
		status = http.StatusConflict
	case fault.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	message := err.Error()
	if code == fault.CodeInternal {
		s.logger.ErrorContext(
			ctx,
			"request failed",
			"error_code",
			code,
			"failure_class",
			observability.Classify(err),
		)
		message = "internal error"
	}
	response := errorResponse{}
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(writer, status, response)
}

func decodeRequest(writer http.ResponseWriter, request *http.Request, target any) error {
	contentType := request.Header.Get("Content-Type")
	if contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		return fault.New(
			fault.CodeInvalidArgument,
			"daemon.decodeRequest",
			"Content-Type must be application/json",
		)
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fault.Wrap(
			fault.CodeInvalidArgument,
			"daemon.decodeRequest",
			"invalid JSON request",
			err,
		)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON documents")
		}
		return fault.Wrap(
			fault.CodeInvalidArgument,
			"daemon.decodeRequest",
			"request must contain one JSON document",
			err,
		)
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func requestLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(writer, request)
		route := request.Pattern
		if route == "" {
			route = "unmatched"
		}
		logger.InfoContext(
			request.Context(),
			"http request",
			"method",
			request.Method,
			"route",
			route,
			"duration_ms",
			time.Since(started).Milliseconds(),
		)
	})
}

// ListenAndServe starts a daemon and stops it when the context is cancelled.
func ListenAndServe(
	ctx context.Context,
	listener net.Listener,
	handler http.Handler,
	shutdownTimeout time.Duration,
	logger *slog.Logger,
) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		result <- err
	}()

	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		if logger != nil {
			logger.Info("shutdown requested")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return <-result
	}
}
