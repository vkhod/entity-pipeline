// Package api exposes the external HTTP contract (stdlib net/http + Go 1.22 routing).
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/store"
)

type Server struct {
	store *store.Store
}

func NewServer(s *store.Store) *Server { return &Server{store: s} }

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /process", s.handleProcess)
	mux.HandleFunc("GET /documents/{id}/status", s.handleStatus)
	mux.HandleFunc("GET /documents/{id}/tokens", s.handleTokens)
	mux.HandleFunc("GET /healthz", s.handleLiveness)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	return mux
}

type processRequest struct {
	DocumentID string `json:"document_id"`
	Text       string `json:"text"`
}

func (s *Server) handleProcess(w http.ResponseWriter, r *http.Request) {
	var req processRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.DocumentID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "document_id and text are required")
		return
	}
	doc, outcome, err := s.store.CreateOrRerun(r.Context(), req.DocumentID, req.Text)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	switch outcome {
	case store.OutcomeConflict:
		writeError(w, http.StatusConflict, "document is already being processed")
	default: // Created or Reran
		writeJSON(w, http.StatusAccepted, statusResponse(doc))
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	doc, found, err := s.store.GetDocument(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	writeJSON(w, http.StatusOK, statusResponse(doc))
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	f := store.TokenFilter{
		Classification: q.Get("classification"),
		Status:         q.Get("status"),
		Limit:          atoiDefault(q.Get("limit"), 100),
		Offset:         atoiDefault(q.Get("offset"), 0),
	}
	if p := q.Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			f.Page = &n
		}
	}
	tokens, err := s.store.ListTokens(r.Context(), id, f)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"document_id": id, "count": len(tokens), "tokens": tokens})
}

// handleLiveness is the Kubernetes liveness probe. It returns 200 as long as the process
// is alive and serving. It must NOT check the database: if it did, a transient Postgres
// blip would make every pod fail its liveness check, causing Kubernetes to restart the
// entire API fleet and turning a short outage into a cascading one.
func (s *Server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz is the Kubernetes readiness probe. It pings Postgres; if the DB is
// unreachable the pod is marked not-ready and removed from the load balancer until
// connectivity is restored. A 2s timeout prevents a hung DB from hanging the probe.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// statusResponse shapes the manifest into the documented status payload, including
// durations computed from the stage timestamps (null until a stage has finished).
func statusResponse(d model.Document) map[string]any {
	resp := map[string]any{
		"document_id": d.ID,
		"status":      d.Status,
		"generation":  d.Generation,
		"progress":    map[string]int{"classified": d.ClassifiedCount, "total": d.TotalTokens},
		"timestamps": map[string]any{
			"extraction_started":       d.ExtractionStartedAt,
			"extraction_completed":     d.ExtractionCompletedAt,
			"classification_started":   d.ClassificationStartedAt,
			"classification_completed": d.ClassificationCompletedAt,
		},
		"durations_ms": map[string]any{
			"extraction":     durationMS(d.ExtractionStartedAt, d.ExtractionCompletedAt),
			"classification": durationMS(d.ClassificationStartedAt, d.ClassificationCompletedAt),
		},
	}
	if d.Error != "" {
		resp["error"] = d.Error
	}
	return resp
}

func durationMS(start, end *time.Time) *int64 {
	if start == nil || end == nil {
		return nil
	}
	ms := end.Sub(*start).Milliseconds()
	return &ms
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
