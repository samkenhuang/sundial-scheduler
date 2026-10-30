package api

import (
	"net/http"
	"strings"
)

type createProjectRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_name", "name is required and must be <= 200 chars")
		return
	}
	project, err := s.store.CreateProject(r.Context(), req.Name, req.Description)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}
