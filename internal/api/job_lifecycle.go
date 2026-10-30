package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
	"scheduler-platform/internal/store"
)

type renameJobRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleRenameJob(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	var req renameJobRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_name", "name is required and must be <= 200 chars")
		return
	}
	before, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if before.ArchivedAt != nil {
		writeArchivedJob(w)
		return
	}
	job, err := s.store.RenameJob(r.Context(), id, req.Name)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "job_name_taken", "job name already exists")
			return
		}
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "job.rename", "job", id.String(), before, job)
	writeJSON(w, http.StatusOK, jobView{Job: job})
}

type archiveJobResponse struct {
	domain.Job
	CanceledRuns int64 `json:"canceledRuns"`
}

func (s *Server) handleArchiveJob(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	before, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	job, canceled, err := s.store.ArchiveJob(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if before.ArchivedAt == nil {
		_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "job.archive", "job", id.String(), before,
			map[string]any{"job": job, "canceledRuns": canceled})
	}
	writeJSON(w, http.StatusOK, archiveJobResponse{Job: job, CanceledRuns: canceled})
}

func (s *Server) rejectArchivedJob(w http.ResponseWriter, r *http.Request, id uuid.UUID) (domain.Job, bool) {
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		fail(w, err)
		return domain.Job{}, true
	}
	if job.ArchivedAt != nil {
		writeArchivedJob(w)
		return job, true
	}
	return job, false
}

func writeArchivedJob(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "job_archived", "job is archived")
}
