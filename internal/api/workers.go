package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
	"scheduler-platform/internal/presence"
	"scheduler-platform/internal/store"
)

type workerRunView struct {
	ID         uuid.UUID `json:"id"`
	JobID      uuid.UUID `json:"jobId"`
	JobName    string    `json:"jobName"`
	LaunchMode string    `json:"launchMode"`
	TimeHour   string    `json:"timeHour"`
	Attempt    int       `json:"attempt"`
	StartedAt  time.Time `json:"startedAt"`
}

type workerView struct {
	domain.Worker
	Labels       []string        `json:"labels"`
	RunningRuns  []workerRunView `json:"runningRuns"`
	RunningCount int             `json:"runningCount"`
}

type workerRunHistoryView struct {
	ID         uuid.UUID  `json:"id"`
	JobID      uuid.UUID  `json:"jobId"`
	JobName    string     `json:"jobName"`
	State      string     `json:"state"`
	LaunchMode string     `json:"launchMode"`
	TimeHour   string     `json:"timeHour"`
	Attempt    int        `json:"attempt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      *string    `json:"error,omitempty"`
}

type workerDetailView struct {
	Worker     workerView             `json:"worker"`
	GatewayURL string                 `json:"gatewayUrl"`
	RecentRuns []workerRunHistoryView `json:"recentRuns"`
}

func toWorkerView(w domain.Worker, running []workerRunView) workerView {
	labels, _ := w.LabelList()
	if running == nil {
		running = []workerRunView{}
	}
	return workerView{Worker: w, Labels: labels, RunningRuns: running, RunningCount: len(running)}
}

func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := s.store.ListWorkers(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	// Presence is the authoritative online signal. It lives in Redis/local
	// cache, so listing workers never depends on per-worker heartbeat DB rows.
	s.bindPresence(r.Context(), workers)

	// One small live-build query per list request. Grouping in memory avoids
	// N per-worker queries and keeps the endpoint useful to the home sidebar.
	runningByWorker := map[uuid.UUID][]workerRunView{}
	running, err := s.store.ListRunningRuns(r.Context(), 5000)
	if err != nil {
		// Worker presence is still useful during a partial DB read failure;
		// do not turn the whole executor panel into an error state.
		s.log.Warn("list running runs", "error", err)
	}
	for _, run := range running {
		runningByWorker[run.WorkerID] = append(runningByWorker[run.WorkerID], workerRunView{
			ID: run.RunID, JobID: run.JobID, JobName: run.JobName,
			LaunchMode: run.LaunchMode, TimeHour: run.TimeHour,
			Attempt: run.Attempt, StartedAt: run.StartedAt,
		})
	}
	out := make([]workerView, 0, len(workers))
	for _, w := range workers {
		out = append(out, toWorkerView(w, runningByWorker[w.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": out})
}

// Bind overlays ephemeral presence onto durable worker rows. DB status is the
// last durable state (ONLINE/DRAINING); presence decides whether it is stale.
func (s *Server) bindPresence(ctx context.Context, workers []domain.Worker) {
	recs, err := s.presence.List(ctx)
	if err != nil {
		s.log.Warn("list presence", "error", err)
	}
	byID := make(map[uuid.UUID]presence.Record, len(recs))
	for _, rec := range recs {
		byID[rec.WorkerID] = rec
	}
	available := s.presence.Available()
	now := time.Now()
	for i := range workers {
		rec, ok := byID[workers[i].ID]
		if ok {
			workers[i].LastHeartbeatAt = rec.LastSeen
			workers[i].LogUsedBytes = rec.LogUsedBytes
			if workers[i].Draining {
				workers[i].Status = "DRAINING"
			} else {
				workers[i].Status = "ONLINE"
			}
			continue
		}
		// Dual-source fallback: a recent DB heartbeat is evidence of life while
		// Redis is unavailable, and covers the Redis restart/warmup window.
		if now.Sub(workers[i].LastHeartbeatAt) <= s.cfg.PresenceTTL {
			if workers[i].Draining {
				workers[i].Status = "DRAINING"
			} else {
				workers[i].Status = "ONLINE"
			}
			continue
		}
		if available || now.Sub(workers[i].LastHeartbeatAt) > s.cfg.PresenceTTL {
			workers[i].Status = "OFFLINE"
		}
	}
}

type workerInputRequest struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Labels           []string `json:"labels"`
	ConcurrencyLimit int      `json:"concurrencyLimit"`
}

func workerInput(req workerInputRequest) (store.WorkerInput, string) {
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 200 {
		return store.WorkerInput{}, "executor name is required and must be <= 200 chars"
	}
	if utf8.RuneCountInString(req.Description) > 2000 {
		return store.WorkerInput{}, "executor description must be <= 2000 chars"
	}
	if req.ConcurrencyLimit < 1 || req.ConcurrencyLimit > 20 {
		return store.WorkerInput{}, "concurrencyLimit must be between 1 and 20"
	}
	return store.WorkerInput{
		Name: name, Description: req.Description,
		Labels: domain.NormalizeLabels(req.Labels), ConcurrencyLimit: req.ConcurrencyLimit,
	}, ""
}

func (s *Server) handleCreateWorker(w http.ResponseWriter, r *http.Request) {
	var req workerInputRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in, msg := workerInput(req)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_executor", msg)
		return
	}
	worker, err := s.store.CreateWorker(r.Context(), in)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "executor_name_taken", "executor name already exists")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "worker.create", "worker", worker.ID.String(), nil, worker)
	writeJSON(w, http.StatusCreated, toWorkerView(worker, nil))
}

func (s *Server) handleGetWorker(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	worker, err := s.store.GetWorker(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	workers := []domain.Worker{worker}
	s.bindPresence(r.Context(), workers)
	running, err := s.store.ListRunningRunsForWorker(r.Context(), id, 20)
	if err != nil {
		fail(w, err)
		return
	}
	recent, err := s.store.ListWorkerRuns(r.Context(), id, 20)
	if err != nil {
		fail(w, err)
		return
	}
	runs := make([]workerRunView, 0, len(running))
	for _, run := range running {
		runs = append(runs, workerRunView{
			ID: run.RunID, JobID: run.JobID, JobName: run.JobName,
			LaunchMode: run.LaunchMode, TimeHour: run.TimeHour,
			Attempt: run.Attempt, StartedAt: run.StartedAt,
		})
	}
	history := make([]workerRunHistoryView, 0, len(recent))
	for _, run := range recent {
		history = append(history, workerRunHistoryView{
			ID: run.RunID, JobID: run.JobID, JobName: run.JobName, State: run.State,
			LaunchMode: run.LaunchMode, TimeHour: run.TimeHour, Attempt: run.Attempt,
			StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Error: run.Error,
		})
	}
	writeJSON(w, http.StatusOK, workerDetailView{
		Worker:     toWorkerView(workers[0], runs),
		GatewayURL: strings.TrimRight(s.cfg.GatewayURL, "/"),
		RecentRuns: history,
	})
}

func (s *Server) handleUpdateWorker(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	var req workerInputRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in, msg := workerInput(req)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_executor", msg)
		return
	}
	before, err := s.store.GetWorker(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	worker, err := s.store.UpdateWorker(r.Context(), id, in)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "executor_name_taken", "executor name already exists")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "worker.update", "worker", id.String(), before, worker)
	writeJSON(w, http.StatusOK, toWorkerView(worker, nil))
}

func (s *Server) handleDeleteWorker(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	before, err := s.store.GetWorker(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.store.DeleteWorker(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrWorkerBusy) {
			writeError(w, http.StatusConflict, "executor_has_running_builds", "executor has running builds; stop them before deleting")
			return
		}
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "worker.delete", "worker", id.String(), before, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

type drainWorkerRequest struct {
	Draining bool `json:"draining"`
}

func (s *Server) handleDrainWorker(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	var req drainWorkerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.store.SetWorkerDraining(r.Context(), id, req.Draining); err != nil {
		fail(w, err)
		return
	}
	worker, err := s.store.GetWorker(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, worker)
}

// --- worker gateway ---

type registerWorkerRequest struct {
	WorkerID         string   `json:"workerId,omitempty"`
	Labels           []string `json:"labels"`
	ConcurrencyLimit int      `json:"concurrencyLimit"`
	Hostname         string   `json:"hostname,omitempty"`
	Version          string   `json:"version,omitempty"`
	LogQuotaBytes    int64    `json:"logQuotaBytes,omitempty"`
}

func (s *Server) handleWorkerRegister(w http.ResponseWriter, r *http.Request) {
	var req registerWorkerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id := uuid.New()
	if req.WorkerID != "" {
		var err error
		id, err = uuid.Parse(req.WorkerID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_worker_id", "workerId must be a UUID")
			return
		}
	}
	wkr, err := s.store.RegisterWorker(r.Context(), store.RegisterWorkerInput{
		ID: id, Labels: req.Labels, ConcurrencyLimit: req.ConcurrencyLimit,
		Hostname: req.Hostname, Version: req.Version, LogQuotaBytes: req.LogQuotaBytes,
	})
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.presence.Touch(r.Context(), presence.Record{
		WorkerID: wkr.ID, LastSeen: time.Now().UTC(), LogUsedBytes: wkr.LogUsedBytes,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"workerId": wkr.ID, "claimWait": "30s",
		"heartbeatInterval": s.cfg.Heartbeat.String(),
	})
}

type claimRequest struct {
	WorkerID     string `json:"workerId"`
	WaitSeconds  int    `json:"waitSeconds,omitempty"`
	LogUsedBytes *int64 `json:"logUsedBytes,omitempty"`
}

type claimRun struct {
	RunID      uuid.UUID      `json:"runId"`
	LeaseToken uuid.UUID      `json:"leaseToken"`
	TimeHour   string         `json:"timeHour"`
	Attempt    int            `json:"attempt"`
	Spec       domain.JobSpec `json:"spec"`

	// Provenance rendered in the build's first log line.
	JobName            string     `json:"jobName"`
	LaunchMode         string     `json:"launchMode"`
	TriggeredByRunID   *uuid.UUID `json:"triggeredByRunId,omitempty"`
	TriggeredByJobName *string    `json:"triggeredByJobName,omitempty"`
}

func (s *Server) handleWorkerClaim(w http.ResponseWriter, r *http.Request) {
	var req claimRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	workerID, err := uuid.Parse(req.WorkerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_worker_id", "workerId must be a UUID")
		return
	}
	wkr, err := s.store.GetWorker(r.Context(), workerID)
	if err != nil {
		s.log.Error("get worker", "worker_id", workerID, "error", err)
		fail(w, err)
		return
	}
	// Claim must not refresh liveness. OFFLINE/DRAINING Workers cannot claim;
	// only a heartbeat can revive an OFFLINE Worker.
	if wkr.Draining || wkr.Status != "ONLINE" {
		writeJSON(w, http.StatusOK, map[string]any{"runs": []claimRun{}, "status": wkr.Status})
		return
	}
	running, err := s.store.RunningCount(r.Context(), workerID)
	if err != nil {
		s.log.Error("count running", "worker_id", workerID, "error", err)
		fail(w, err)
		return
	}
	free := wkr.ConcurrencyLimit - running
	if free <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{"runs": []claimRun{}})
		return
	}
	labels, err := wkr.LabelList()
	if err != nil {
		fail(w, err)
		return
	}
	wait := time.Duration(req.WaitSeconds) * time.Second
	if wait > 25*time.Second {
		wait = 25 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		claimed, err := s.store.ClaimRuns(r.Context(), workerID, labels, free)
		if err != nil {
			s.log.Error("claim runs", "worker_id", workerID, "error", err)
			fail(w, err)
			return
		}
		if len(claimed) > 0 {
			out := make([]claimRun, 0, len(claimed))
			for _, c := range claimed {
				out = append(out, claimRun{
					RunID: c.RunID, LeaseToken: c.LeaseToken, TimeHour: c.TimeHour, Attempt: c.Attempt, Spec: c.Spec,
					JobName: c.JobName, LaunchMode: c.LaunchMode,
					TriggeredByRunID: c.TriggeredByRunID, TriggeredByJobName: c.TriggeredByJobName,
				})
			}
			writeJSON(w, http.StatusOK, map[string]any{"runs": out})
			return
		}
		if wait <= 0 || time.Now().After(deadline) {
			writeJSON(w, http.StatusOK, map[string]any{"runs": []claimRun{}})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type workerHeartbeatRequest struct {
	WorkerID     string      `json:"workerId"`
	Running      []uuid.UUID `json:"runningRunIds,omitempty"`
	LogUsedBytes *int64      `json:"logUsedBytes,omitempty"`
}

func (s *Server) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req workerHeartbeatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	workerID, err := uuid.Parse(req.WorkerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_worker_id", "workerId must be a UUID")
		return
	}
	used := int64(0)
	if req.LogUsedBytes != nil {
		used = *req.LogUsedBytes
	}
	// A heartbeat is the only operation that revives a durable OFFLINE state.
	if err := s.store.HeartbeatWorker(r.Context(), workerID); err != nil {
		fail(w, err)
		return
	}
	s.touchWorkerPresence(r.Context(), workerID, used)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// touchWorkerPresence updates shared Redis presence. Only when Redis is
// unavailable does it fall back to one Worker-level DB heartbeat. Worker
// liveness is Redis-first; DB is the failure-path fallback.
func (s *Server) touchWorkerPresence(ctx context.Context, workerID uuid.UUID, used int64) {
	if err := s.presence.Touch(ctx, presence.Record{
		WorkerID: workerID, LastSeen: time.Now().UTC(), LogUsedBytes: used,
	}); err != nil {
		s.log.Warn("touch presence", "worker_id", workerID, "error", err)
	}
	if s.presence.Available() {
		return
	}
	if err := s.store.TouchWorkerHeartbeat(ctx, workerID); err != nil {
		s.log.Warn("touch worker DB heartbeat", "worker_id", workerID, "error", err)
	}
}

type completeRunRequest struct {
	WorkerID   string    `json:"workerId"`
	LeaseToken uuid.UUID `json:"leaseToken"`
	ExitCode   *int      `json:"exitCode"`
	TimedOut   bool      `json:"timedOut"`
	Error      string    `json:"error,omitempty"`
}

func (s *Server) handleWorkerComplete(w http.ResponseWriter, r *http.Request) {
	runID, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	var req completeRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	workerID, err := uuid.Parse(req.WorkerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_worker_id", "workerId must be a UUID")
		return
	}
	result, err := s.store.CompleteRun(r.Context(), store.CompleteRunInput{
		RunID: runID, WorkerID: workerID, LeaseToken: req.LeaseToken,
		ExitCode: req.ExitCode, TimedOut: req.TimedOut, Error: req.Error,
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": result.State, "attempt": result.Attempt, "ignored": result.Ignored})
}

func (s *Server) handleWorkerCommands(w http.ResponseWriter, r *http.Request) {
	workerID, err := uuid.Parse(r.URL.Query().Get("workerId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_worker_id", "workerId must be a UUID")
		return
	}
	wait := 30 * time.Second
	if raw := r.URL.Query().Get("wait"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			wait = d
		}
	}
	cmd, err := s.store.PollCommands(r.Context(), workerID, wait)
	if err != nil {
		fail(w, err)
		return
	}
	if cmd == nil {
		writeJSON(w, http.StatusOK, map[string]any{"commands": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": []domain.WorkerCommand{*cmd}})
}

type commandResultRequest struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleWorkerCommandResult(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	var req commandResultRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.State == "" {
		req.State = "DONE"
	}
	if err := s.store.CompleteCommand(r.Context(), store.CompleteCommandInput{ID: id, State: req.State, Error: req.Error}); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": req.State})
}
