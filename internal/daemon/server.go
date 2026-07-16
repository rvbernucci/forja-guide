// Package daemon exposes the Sprint 01 HTTP control-plane boundary.
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
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
	"github.com/rvbernucci/forja-guide/internal/version"
)

const maxRequestBody = 1 << 20

// IDGenerator creates run identifiers at the external command boundary.
type IDGenerator func() (identity.RunID, error)

// Server is the in-memory Sprint 01 daemon.
type Server struct {
	store    *runstate.Store
	registry *contracts.Registry
	newID    IDGenerator
	logger   *slog.Logger
	ready    atomic.Bool
}

// New creates a daemon with explicit dependencies.
func New(
	store *runstate.Store,
	registry *contracts.Registry,
	newID IDGenerator,
	logger *slog.Logger,
) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if registry == nil {
		return nil, fmt.Errorf("contract registry is required")
	}
	if newID == nil {
		newID = identity.NewRunID
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	server := &Server{
		store:    store,
		registry: registry,
		newID:    newID,
		logger:   logger,
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
	mux.HandleFunc("POST /v1/runs", s.handleCreateRun)
	mux.HandleFunc("GET /v1/runs/{run_id}", s.handleGetRun)
	mux.HandleFunc("POST /v1/runs/{run_id}/transitions", s.handleTransitionRun)
	return requestLogMiddleware(s.logger, mux)
}

// SetReady changes readiness without changing liveness.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(writer http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready",
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleVersion(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, version.Current())
}

type createRunRequest struct {
	Objective string `json:"objective"`
}

func (s *Server) handleCreateRun(writer http.ResponseWriter, request *http.Request) {
	var command createRunRequest
	if err := decodeRequest(writer, request, &command); err != nil {
		s.writeError(writer, err)
		return
	}
	id, err := s.newID()
	if err != nil {
		s.writeError(writer, fault.Wrap(
			fault.CodeInternal,
			"daemon.createRun",
			"generate run identifier",
			err,
		))
		return
	}
	run, err := s.store.Create(id, command.Objective)
	if err != nil {
		s.writeError(writer, err)
		return
	}
	if err := s.validateRun(run); err != nil {
		s.writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, run)
}

func (s *Server) handleGetRun(writer http.ResponseWriter, request *http.Request) {
	id, err := identity.ParseRunID(request.PathValue("run_id"))
	if err != nil {
		s.writeError(writer, fault.Wrap(
			fault.CodeInvalidArgument,
			"daemon.getRun",
			"invalid run identifier",
			err,
		))
		return
	}
	run, err := s.store.Get(id)
	if err != nil {
		s.writeError(writer, err)
		return
	}
	if err := s.validateRun(run); err != nil {
		s.writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, run)
}

type transitionRunRequest struct {
	ExpectedVersion int    `json:"expected_version"`
	TargetState     string `json:"target_state"`
}

func (s *Server) handleTransitionRun(writer http.ResponseWriter, request *http.Request) {
	id, err := identity.ParseRunID(request.PathValue("run_id"))
	if err != nil {
		s.writeError(writer, fault.Wrap(
			fault.CodeInvalidArgument,
			"daemon.transitionRun",
			"invalid run identifier",
			err,
		))
		return
	}
	var command transitionRunRequest
	if err := decodeRequest(writer, request, &command); err != nil {
		s.writeError(writer, err)
		return
	}
	if command.ExpectedVersion < 1 {
		s.writeError(writer, fault.New(
			fault.CodeInvalidArgument,
			"daemon.transitionRun",
			"expected_version must be at least 1",
		))
		return
	}
	target, err := runstate.ParseState(command.TargetState)
	if err != nil {
		s.writeError(writer, err)
		return
	}
	run, err := s.store.Transition(id, command.ExpectedVersion, target)
	if err != nil {
		s.writeError(writer, err)
		return
	}
	if err := s.validateRun(run); err != nil {
		s.writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, run)
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

func (s *Server) writeError(writer http.ResponseWriter, err error) {
	code := fault.CodeOf(err)
	status := http.StatusInternalServerError
	switch code {
	case fault.CodeInvalidArgument:
		status = http.StatusBadRequest
	case fault.CodeNotFound:
		status = http.StatusNotFound
	case fault.CodeConflict:
		status = http.StatusConflict
	case fault.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	message := err.Error()
	if code == fault.CodeInternal {
		s.logger.Error("request failed", "error", err)
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
		logger.InfoContext(
			request.Context(),
			"http request",
			"method",
			request.Method,
			"path",
			request.URL.Path,
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
