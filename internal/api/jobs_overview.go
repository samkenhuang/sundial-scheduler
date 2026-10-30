package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
	"scheduler-platform/internal/store"
)

type jobLastRunView struct {
	RunID      uuid.UUID  `json:"id"`
	State      string     `json:"state"`
	LaunchMode string     `json:"launchMode"`
	TimeHour   string     `json:"timeHour"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      *string    `json:"error,omitempty"`
}

type jobOverviewView struct {
	domain.Job
	LastRun *jobLastRunView `json:"lastRun,omitempty"`
}

func (s *Server) handleListJobsOverview(w http.ResponseWriter, r *http.Request) {
	f := store.JobFilter{Limit: 100}
	f.Query = r.URL.Query().Get("q")
	if raw := r.URL.Query().Get("projectId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_project", "projectId must be a UUID")
			return
		}
		f.ProjectID = &id
	}
	jobs, err := s.store.ListJobs(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	ids := make([]uuid.UUID, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	latest, err := s.store.LatestRunsByJob(r.Context(), ids)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]jobOverviewView, 0, len(jobs))
	for _, j := range jobs {
		v := jobOverviewView{Job: j}
		if last, ok := latest[j.ID]; ok {
			v.LastRun = &jobLastRunView{
				RunID: last.RunID, State: last.State, LaunchMode: last.LaunchMode,
				TimeHour: last.TimeHour, CreatedAt: last.CreatedAt,
				StartedAt: last.StartedAt, FinishedAt: last.FinishedAt, Error: last.Error,
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}
