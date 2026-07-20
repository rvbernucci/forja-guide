package alpha

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

const maxRequestBody = 32 << 10

//go:embed web/*
var embeddedWeb embed.FS

type Handler struct {
	service *Service
	web     http.Handler
}

func NewHandler(service *Service) (http.Handler, error) {
	webRoot, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		return nil, err
	}
	handler := &Handler{service: service, web: http.FileServer(http.FS(webRoot))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	mux.HandleFunc("GET /readyz", handler.ready)
	mux.HandleFunc("GET /api/v1/bootstrap", handler.bootstrap)
	mux.HandleFunc("POST /api/v1/research", handler.startResearch)
	mux.HandleFunc("GET /api/v1/research/{researchID}", handler.research)
	mux.Handle("/", handler.web)
	return securityHeaders(mux), nil
}

func (h *Handler) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
}

func (h *Handler) ready(writer http.ResponseWriter, _ *http.Request) {
	bootstrap := h.service.Bootstrap()
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":               "ready",
		"interface_ready":      true,
		"core_inference_ready": bootstrap.Runtime.CoreInferenceReady,
	})
}

func (h *Handler) bootstrap(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, h.service.Bootstrap())
}

func (h *Handler) startResearch(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input ResearchRequest
	if err := decoder.Decode(&input); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "request body too large") {
			status = http.StatusRequestEntityTooLarge
		}
		writeAPIError(writer, status, "invalid_request", "Request body must contain only a string prompt.")
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request", "Request body must contain exactly one JSON object.")
		return
	}
	session, err := h.service.StartResearch(input.Prompt)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_prompt", err.Error())
		return
	}
	writer.Header().Set("Location", "/api/v1/research/"+session.ID)
	writeJSON(writer, http.StatusCreated, session)
}

func (h *Handler) research(writer http.ResponseWriter, request *http.Request) {
	id := strings.TrimSpace(request.PathValue("researchID"))
	if id == "" {
		writeAPIError(writer, http.StatusBadRequest, "invalid_research_id", "Research id is required.")
		return
	}
	session, ok := h.service.Research(id)
	if !ok {
		writeAPIError(writer, http.StatusNotFound, "research_not_found", "Research session was not found.")
		return
	}
	writeJSON(writer, http.StatusOK, session)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("additional JSON value")
	}
	return err
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}

func writeAPIError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]APIError{"error": {Code: code, Message: message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
