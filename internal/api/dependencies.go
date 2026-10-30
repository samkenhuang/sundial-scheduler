package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"scheduler-platform/internal/store"
)

type dependencyInput struct {
	UpstreamJobID string `json:"upstreamJobId"`
	Condition     string `json:"condition,omitempty"`
	Required      *bool  `json:"required,omitempty"`
}

type setDependenciesRequest struct {
	Upstreams []dependencyInput `json:"upstreams"`
}

func (s *Server) handleGetJobDependencies(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	upstreams, downstreams, err := s.store.ListJobDependencies(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"upstreams":   upstreams,
		"downstreams": downstreams,
		"cycleFormat": job.CycleFormat,
	})
}

func (s *Server) handleSetJobDependencies(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	if _, rejected := s.rejectArchivedJob(w, r, id); rejected {
		return
	}
	var req setDependenciesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	edges := make([]store.DependencyEdge, 0, len(req.Upstreams))
	for _, in := range req.Upstreams {
		upstream, err := uuid.Parse(strings.TrimSpace(in.UpstreamJobID))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_dependency", "upstreamJobId must be a UUID")
			return
		}
		condition := in.Condition
		if condition == "" {
			condition = "on_success"
		}
		required := true
		if in.Required != nil {
			required = *in.Required
		}
		edges = append(edges, store.DependencyEdge{
			UpstreamJobID: upstream, Condition: condition, Required: required,
		})
	}
	beforeUp, beforeDown, _ := s.store.ListJobDependencies(r.Context(), id)
	if _, err := s.store.SetJobDependencies(r.Context(), id, edges); err != nil {
		fail(w, err)
		return
	}
	upstreams, downstreams, err := s.store.ListJobDependencies(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "job.dependencies.update", "job", id.String(),
		map[string]any{"upstreams": beforeUp, "downstreams": beforeDown},
		map[string]any{"upstreams": upstreams, "downstreams": downstreams})
	writeJSON(w, http.StatusOK, map[string]any{"upstreams": upstreams, "downstreams": downstreams})
}

func (s *Server) handleGetDependencyGraph(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	depth := 3
	if raw := strings.TrimSpace(r.URL.Query().Get("depth")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > store.MaxDependencyGraphDepth {
			writeError(w, http.StatusBadRequest, "invalid_depth", "depth must be an integer from 1 to 5")
			return
		}
		depth = parsed
	}
	if _, err := s.store.GetJob(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	graph, err := s.store.GetDependencyGraph(r.Context(), id, depth)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, graph)
}
