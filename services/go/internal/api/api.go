// Package api wires the four /v1 endpoints onto a *mongo.Client.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/mongodb-labs/mongo-drivers-benchmark/services/go/internal/ops"
)

// Info is the static portion of /v1/info.
type Info struct {
	Driver          string
	DriverVersion   string
	Language        string
	LanguageVersion string
	SpecVersion     string
}

// Server is an http.Handler that serves the four /v1 endpoints.
type Server struct {
	client *mongo.Client
	info   Info
	disp   *ops.Dispatcher
	mux    *http.ServeMux
}

// NewServer constructs a new Server.
func NewServer(client *mongo.Client, info Info) *Server {
	s := &Server{
		client: client,
		info:   info,
		disp:   &ops.Dispatcher{Client: client},
		mux:    http.NewServeMux(),
	}
	s.mux.HandleFunc("/v1/ops", s.handleOps)
	s.mux.HandleFunc("/v1/admin/reset", s.handleReset)
	s.mux.HandleFunc("/v1/info", s.handleInfo)
	s.mux.HandleFunc("/v1/health", s.handleHealth)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	s.mux.ServeHTTP(w, r)
}

// ---- /v1/ops ----

func (s *Server) handleOps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "BAD_REQUEST", "method not allowed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("read body: %v", err))
		return
	}
	req, decoded, verr := ops.DecodeAndValidate(body)
	if verr != nil {
		writeError(w, http.StatusBadRequest, verr.Code, verr.Message)
		return
	}

	results := s.disp.Run(r.Context(), *req.Database, decoded)
	resp := ops.Response{Results: results}

	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil {
		// Headers already written; nothing useful to send back.
		return
	}
}

// ---- /v1/admin/reset ----

type resetRequest struct {
	Databases []string `json:"databases"`
}

type resetResponse struct {
	Dropped []string `json:"dropped"`
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "BAD_REQUEST", "method not allowed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("read body: %v", err))
		return
	}
	var req resetRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "SCHEMA_VIOLATION", fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if len(req.Databases) == 0 {
		writeError(w, http.StatusBadRequest, "EMPTY_OPS", "databases must be a non-empty array")
		return
	}
	for _, name := range req.Databases {
		if name == "" {
			writeError(w, http.StatusBadRequest, "SCHEMA_VIOLATION", "database name must be non-empty")
			return
		}
		switch name {
		case "admin", "local", "config":
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("refusing to drop %q", name))
			return
		}
	}
	dropped := make([]string, 0, len(req.Databases))
	for _, name := range req.Databases {
		if err := s.client.Database(name).Drop(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "BAD_REQUEST", fmt.Sprintf("drop %s: %v", name, err))
			return
		}
		dropped = append(dropped, name)
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resetResponse{Dropped: dropped})
}

// ---- /v1/info ----

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "BAD_REQUEST", "method not allowed")
		return
	}
	out := struct {
		Driver          string `json:"driver"`
		DriverVersion   string `json:"driver_version"`
		Language        string `json:"language"`
		LanguageVersion string `json:"language_version"`
		SpecVersion     string `json:"spec_version"`
	}{
		Driver:          s.info.Driver,
		DriverVersion:   s.info.DriverVersion,
		Language:        s.info.Language,
		LanguageVersion: s.info.LanguageVersion,
		SpecVersion:     s.info.SpecVersion,
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// ---- /v1/health ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "BAD_REQUEST", "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.client.Ping(ctx, readpref.Primary()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "detail": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// ---- helpers ----

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": msg,
		},
	})
}

