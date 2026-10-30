package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
	"scheduler-platform/internal/store"
)

type createJobRequest struct {
	Name        string  `json:"name"`
	ProjectID   *string `json:"projectId,omitempty"`
	CycleFormat string  `json:"cycleFormat,omitempty"`
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_name", "name is required and must be <= 200 chars")
		return
	}
	var projectID *uuid.UUID
	if req.ProjectID != nil && *req.ProjectID != "" {
		id, err := uuid.Parse(*req.ProjectID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_project", "projectId must be a UUID")
			return
		}
		if _, err := s.store.GetProject(r.Context(), id); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_project", "projectId does not reference an existing project")
			return
		}
		projectID = &id
	}
	if req.CycleFormat == "" {
		req.CycleFormat = "2006-01-02"
	}
	if err := domain.ValidateCycleFormat(req.CycleFormat); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cycle_format", err.Error())
		return
	}
	job, err := s.store.CreateJob(r.Context(), store.CreateJobInput{
		Name: req.Name, ProjectID: projectID, CreatedBy: actorFromContext(r.Context()),
		CycleFormat: req.CycleFormat,
	})
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "job.create", "job", job.ID.String(), nil, job)
	writeJSON(w, http.StatusCreated, jobView{Job: job})
}

type jobVersionView struct {
	ID        uuid.UUID       `json:"id"`
	JobID     uuid.UUID       `json:"jobId"`
	Version   int             `json:"version"`
	Spec      json.RawMessage `json:"spec"`
	SpecHash  string          `json:"specHash"`
	CreatedBy string          `json:"createdBy"`
	CreatedAt time.Time       `json:"createdAt"`
}

func versionView(v domain.JobVersion) jobVersionView {
	return jobVersionView{
		ID: v.ID, JobID: v.JobID, Version: v.Version, Spec: json.RawMessage(v.Spec),
		SpecHash: v.SpecHash, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt,
	}
}

type jobView struct {
	domain.Job
	CurrentVersion *jobVersionView `json:"currentVersion,omitempty"`
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
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
	view := jobView{Job: job}
	if job.CurrentVersionID != nil {
		if v, err := s.store.GetCurrentJobVersion(r.Context(), job.ID); err == nil {
			vv := versionView(v)
			view.CurrentVersion = &vv
		}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	f := store.JobFilter{Limit: 50}
	if raw := r.URL.Query().Get("projectId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_project", "projectId must be a UUID")
			return
		}
		f.ProjectID = &id
	}
	f.Query = r.URL.Query().Get("q")
	jobs, err := s.store.ListJobs(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

type publishVersionRequest struct {
	Spec domain.JobSpec `json:"spec"`
}

func (s *Server) handlePublishVersion(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	if _, rejected := s.rejectArchivedJob(w, r, id); rejected {
		return
	}
	var req publishVersionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Spec.Normalize()
	if err := req.Spec.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_spec", err.Error())
		return
	}
	hash, err := req.Spec.Hash()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "cannot hash spec")
		return
	}
	version, err := s.store.PublishJobVersion(r.Context(), id, req.Spec, hash, actorFromContext(r.Context()))
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "job.version.publish", "job", id.String(), nil, version)
	writeJSON(w, http.StatusCreated, versionView(version))
}

type updateScheduleRequest struct {
	Cron            *string `json:"cron"`
	Timezone        string  `json:"timezone,omitempty"`
	ScheduleEnabled bool    `json:"scheduleEnabled"`
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	if _, rejected := s.rejectArchivedJob(w, r, id); rejected {
		return
	}
	var req updateScheduleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}
	var next *time.Time
	if req.ScheduleEnabled {
		if req.Cron == nil || strings.TrimSpace(*req.Cron) == "" {
			writeError(w, http.StatusBadRequest, "invalid_schedule", "cron is required when scheduleEnabled is true")
			return
		}
		*req.Cron = strings.TrimSpace(*req.Cron)
		n, err := domain.NextFire(*req.Cron, req.Timezone, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_schedule", err.Error())
			return
		}
		next = &n
	}
	before, _ := s.store.GetJob(r.Context(), id)
	job, err := s.store.UpdateSchedule(r.Context(), id, store.UpdateScheduleInput{
		Cron: req.Cron, Timezone: req.Timezone, ScheduleEnabled: req.ScheduleEnabled, NextFireAt: next,
	})
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "job.schedule.update", "job", id.String(), before, job)
	writeJSON(w, http.StatusOK, jobView{Job: job})
}

type updateLogRetentionRequest struct {
	Runs int `json:"runs"`
	Days int `json:"days"`
}

func (s *Server) handleUpdateLogRetention(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	if _, rejected := s.rejectArchivedJob(w, r, id); rejected {
		return
	}
	var req updateLogRetentionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Runs < 1 || req.Runs > 10000 {
		writeError(w, http.StatusBadRequest, "invalid_retention", "runs must be between 1 and 10000")
		return
	}
	if req.Days < 1 || req.Days > 3650 {
		writeError(w, http.StatusBadRequest, "invalid_retention", "days must be between 1 and 3650")
		return
	}
	before, _ := s.store.GetJob(r.Context(), id)
	job, err := s.store.UpdateLogRetention(r.Context(), id, req.Runs, req.Days)
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "job.log_retention.update", "job", id.String(), before, job)
	writeJSON(w, http.StatusOK, jobView{Job: job})
}

type triggerRunRequest struct {
	Inputs         any    `json:"inputs,omitempty"`
	TimeHour       string `json:"timeHour,omitempty"`
	CycleKey       string `json:"cycleKey,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

func (s *Server) handleTriggerRun(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	var req triggerRunRequest
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	job, rejected := s.rejectArchivedJob(w, r, id)
	if rejected {
		return
	}
	if job.CurrentVersionID == nil {
		writeError(w, http.StatusConflict, "no_version", "job has no published version")
		return
	}
	version, err := s.store.GetCurrentJobVersion(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	var spec domain.JobSpec
	if err := jsonUnmarshal(version.Spec, &spec); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "invalid stored job spec")
		return
	}
	idem := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idem == "" {
		idem = strings.TrimSpace(req.IdempotencyKey)
	}
	var idemPtr, cyclePtr *string
	if idem != "" {
		idemPtr = &idem
	}
	cycleKey := strings.TrimSpace(req.CycleKey)
	if cycleKey == "" {
		cycleKey, err = domain.CycleKey(job.CycleFormat, job.Timezone, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invalid_cycle_config", "job cycle configuration is invalid")
			return
		}
	}
	cyclePtr = &cycleKey
	timeHour := strings.TrimSpace(req.TimeHour)
	if timeHour == "" {
		timeHour, err = domain.TimeHourAt(time.Now().UTC(), job.Timezone)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invalid_time_hour", "job timezone is invalid")
			return
		}
	}
	if err := domain.ValidateTimeHour(timeHour); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_time_hour", err.Error())
		return
	}
	result, err := s.store.CreateRun(r.Context(), store.CreateRunInput{
		JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api",
		Priority: spec.Priority,
		Spec:     spec, Inputs: req.Inputs, TimeHour: timeHour, CycleKey: cyclePtr, IdempotencyKey: idemPtr,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "idempotency_conflict", "run already exists for this idempotency key")
			return
		}
		fail(w, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
		_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "run.trigger", "run", result.Run.ID.String(), nil, result.Run)
	}
	writeJSON(w, status, result.Run)
}
