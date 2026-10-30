package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
	"scheduler-platform/internal/store"
)

type runView struct {
	domain.Run
	JobName        string          `json:"jobName,omitempty"`
	Spec           json.RawMessage `json:"spec,omitempty"`
	Inputs         json.RawMessage `json:"inputs,omitempty"`
	RequiredLabels []string        `json:"requiredLabels,omitempty"`
}

func (s *Server) runView(r *http.Request, run domain.Run) runView {
	v := runView{Run: run}
	if len(run.CommandSnapshot) > 0 {
		v.Spec = json.RawMessage(run.CommandSnapshot)
	}
	if len(run.Inputs) > 0 {
		v.Inputs = json.RawMessage(run.Inputs)
	}
	v.RequiredLabels, _ = run.Labels()
	if job, err := s.store.GetJob(r.Context(), run.JobID); err == nil {
		v.JobName = job.Name
	}
	return v
}

type runListItem struct {
	domain.Run
	JobName string `json:"jobName,omitempty"`
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	f := store.RunFilter{Limit: 50}
	if raw := r.URL.Query().Get("jobId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_job", "jobId must be a UUID")
			return
		}
		f.JobID = &id
	}
	if raw := r.URL.Query().Get("state"); raw != "" {
		state := domain.RunState(raw)
		f.State = &state
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
			return
		}
		f.Limit = n
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err := store.DecodeRunCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor is invalid")
			return
		}
		f.Cursor = &cursor
	}
	page, err := s.store.ListRuns(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	runs := page.Runs
	ids := make([]uuid.UUID, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.JobID)
	}
	names, err := s.store.JobNames(r.Context(), ids)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]runListItem, 0, len(runs))
	for _, run := range runs {
		out = append(out, runListItem{Run: run, JobName: names[run.JobID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out, "nextCursor": page.NextCursor})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.runView(r, run))
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	run, err := s.store.CancelRun(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "run.cancel", "run", id.String(), nil, run)
	status := http.StatusOK
	if run.State == domain.RunRunning {
		status = http.StatusAccepted
	}
	writeJSON(w, status, s.runView(r, run))
}
