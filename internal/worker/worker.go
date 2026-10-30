package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"scheduler-platform/internal/config"
	"scheduler-platform/internal/domain"
)

type Worker struct {
	cfg  config.Config
	log  *slog.Logger
	cli  *client
	id   uuid.UUID
	host string

	mu      sync.Mutex
	running map[uuid.UUID]context.CancelFunc
	done    map[uuid.UUID]chan struct{}
	streams map[uuid.UUID]context.CancelFunc
	sem     chan struct{}
	logHigh atomic.Bool
	logUsed atomic.Int64
}

type claimedRun struct {
	RunID      uuid.UUID      `json:"runId"`
	LeaseToken uuid.UUID      `json:"leaseToken"`
	TimeHour   string         `json:"timeHour"`
	Attempt    int            `json:"attempt"`
	Spec       domain.JobSpec `json:"spec"`

	// Provenance rendered as the first log line.
	JobName            string     `json:"jobName"`
	LaunchMode         string     `json:"launchMode"`
	TriggeredByRunID   *uuid.UUID `json:"triggeredByRunId,omitempty"`
	TriggeredByJobName *string    `json:"triggeredByJobName,omitempty"`
}

type claimResponse struct {
	Runs []claimedRun `json:"runs"`
}

type commandResponse struct {
	Commands []struct {
		ID      uuid.UUID       `json:"id"`
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	} `json:"commands"`
}

func New(cfg config.Config, log *slog.Logger) *Worker {
	id, err := uuid.Parse(cfg.WorkerID)
	if err != nil {
		id = uuid.New()
	}
	host, _ := os.Hostname()
	return &Worker{
		cfg: cfg, log: log, cli: newClient(cfg.GatewayURL, cfg.WorkerToken), id: id, host: host,
		running: map[uuid.UUID]context.CancelFunc{},
		done:    map[uuid.UUID]chan struct{}{},
		streams: map[uuid.UUID]context.CancelFunc{},
		sem:     make(chan struct{}, cfg.WorkerConcurrency),
	}
}

func (w *Worker) Run(ctx context.Context) error {
	if err := w.register(ctx); err != nil {
		return err
	}
	w.log.Info("worker started", "worker_id", w.id, "concurrency", w.cfg.WorkerConcurrency, "gateway", w.cfg.GatewayURL)
	// Startup order is register -> heartbeat -> claim. The heartbeat revives a
	// durable OFFLINE row before the first claim attempt.
	w.heartbeat(ctx)

	heartbeat := time.NewTicker(w.cfg.Heartbeat)
	defer heartbeat.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				w.heartbeat(ctx)
			}
		}
	}()
	go w.commandLoop(ctx)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				w.flushSpool(ctx)
			}
		}
	}()
	go w.logMaintenance(ctx)

	for {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			for _, cancel := range w.running {
				cancel()
			}
			w.mu.Unlock()
			return nil
		default:
		}
		if len(w.sem) >= w.cfg.WorkerConcurrency {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if w.logHigh.Load() {
			time.Sleep(time.Second)
			continue
		}
		free := w.cfg.WorkerConcurrency - len(w.sem)
		claimed, err := w.claim(ctx, free)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.log.Warn("claim failed", "error", err)
			time.Sleep(time.Second)
			continue
		}
		if len(claimed.Runs) == 0 {
			continue
		}
		for _, run := range claimed.Runs {
			w.sem <- struct{}{}
			go func(run claimedRun) {
				defer func() { <-w.sem }()
				w.execute(ctx, run)
			}(run)
		}
	}
}

func (w *Worker) register(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		var resp struct {
			HeartbeatInterval string `json:"heartbeatInterval"`
		}
		status, err := w.cli.do(ctx, "POST", "/worker/v1/register", map[string]any{
			"workerId": w.id, "labels": w.cfg.WorkerLabels, "concurrencyLimit": w.cfg.WorkerConcurrency,
			"hostname": w.host, "version": "p0", "logQuotaBytes": w.cfg.LogQuotaBytes,
		}, &resp)
		if err == nil && status < 300 {
			// The Gateway owns the heartbeat cadence. Use its returned interval
			// so server-side TTL and worker cadence cannot drift.
			if d, err := time.ParseDuration(resp.HeartbeatInterval); err == nil && d > 0 {
				w.cfg.Heartbeat = d
			}
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("register worker: %w", lastErr)
}

func (w *Worker) claim(ctx context.Context, slots int) (claimResponse, error) {
	var out claimResponse
	status, err := w.cli.do(ctx, "POST", "/worker/v1/claim", map[string]any{
		"workerId": w.id, "waitSeconds": 25, "logUsedBytes": w.logUsed.Load(),
	}, &out)
	if err != nil {
		return out, err
	}
	if status >= 300 {
		return out, fmt.Errorf("claim status %d", status)
	}
	return out, nil
}

func (w *Worker) heartbeat(ctx context.Context) {
	// Presence-only heartbeat. Cancellation is delivered through the worker
	// command channel, not through a per-run lease renewal.
	status, err := w.cli.do(ctx, "POST", "/worker/v1/heartbeat", map[string]any{
		"workerId": w.id, "logUsedBytes": w.logUsed.Load(),
	}, nil)
	if err != nil || status >= 300 {
		if err != nil {
			w.log.Warn("heartbeat failed", "error", err)
		} else {
			w.log.Warn("heartbeat rejected", "status", status)
		}
	}
}

func (w *Worker) commandLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		var out commandResponse
		status, err := w.cli.do(ctx, "GET", "/worker/v1/commands?workerId="+w.id.String()+"&wait=30s", nil, &out)
		if err != nil || status >= 300 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, cmd := range out.Commands {
			w.handleCommand(ctx, cmd.ID, cmd.Kind, cmd.Payload)
		}
	}
}

func (w *Worker) handleCommand(ctx context.Context, id uuid.UUID, kind string, payload json.RawMessage) {
	switch kind {
	case "cancel":
		var p struct {
			RunID uuid.UUID `json:"runId"`
		}
		_ = json.Unmarshal(payload, &p)
		w.mu.Lock()
		cancel := w.running[p.RunID]
		w.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case "log_cleanup":
		var p struct {
			Before string `json:"before,omitempty"`
		}
		_ = json.Unmarshal(payload, &p)
		before := time.Now().Add(-w.cfg.LogRetention)
		if p.Before != "" {
			if t, err := time.Parse(time.RFC3339, p.Before); err == nil {
				before = t
			}
		}
		deleted, bytes, err := cleanupLogs(w.cfg.LogRoot, before)
		if err != nil {
			w.log.Warn("log cleanup failed", "error", err)
		} else {
			w.log.Info("log cleanup complete", "deleted_files", deleted, "deleted_bytes", bytes)
			w.addLogUsage(-bytes)
		}
	case "start_log_stream":
		var p logStreamCommand
		if err := json.Unmarshal(payload, &p); err != nil {
			w.postCommandResult(ctx, id, "FAILED", err.Error())
			return
		}
		if err := w.startLogStream(ctx, p); err != nil {
			w.log.Warn("start log stream failed", "run_id", p.RunID, "error", err)
			w.postCommandResult(ctx, id, "FAILED", err.Error())
			return
		}
	}
	_, _ = w.cli.do(ctx, "POST", "/worker/v1/commands/"+id.String()+"/result", map[string]any{"state": "DONE"}, nil)
}

func (w *Worker) logMaintenance(ctx context.Context) {
	// One full scan on startup establishes the baseline. After that, new log
	// bytes and deleted bytes are accounted incrementally: walking millions of
	// run log files every 30s would itself become the bottleneck.
	w.refreshLogUsage()
	retentionTicker := time.NewTicker(time.Hour)
	defer retentionTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-retentionTicker.C:
			before := time.Now().Add(-w.cfg.LogRetention)
			deleted, bytes, err := cleanupLogs(w.cfg.LogRoot, before)
			if err != nil {
				w.log.Warn("automatic log cleanup failed", "error", err)
				continue
			}
			if deleted > 0 {
				w.log.Info("automatic log cleanup", "deleted_files", deleted, "deleted_bytes", bytes)
				w.addLogUsage(-bytes)
			}
		}
	}
}

func (w *Worker) refreshLogUsage() {
	used, err := measureLogUsage(w.cfg.LogRoot)
	if err != nil {
		w.log.Warn("measure log usage", "error", err)
		return
	}
	w.logUsed.Store(used)
	w.evaluateLogUsage(used)
}

func (w *Worker) addLogUsage(delta int64) {
	if delta == 0 {
		return
	}
	used := w.logUsed.Add(delta)
	if used < 0 {
		w.logUsed.Store(0)
		used = 0
	}
	w.evaluateLogUsage(used)
}

// trackLogFile accounts for bytes appended to one attempt log without walking
// the whole log tree. It is called after the child has exited and the log has
// been synced/uploaded.
func (w *Worker) trackLogFile(path string, initialSize int64) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if delta := info.Size() - initialSize; delta > 0 {
		w.addLogUsage(delta)
	}
}

func (w *Worker) evaluateLogUsage(used int64) {
	if w.cfg.LogQuotaBytes <= 0 {
		w.logHigh.Store(false)
		return
	}
	ratio := float64(used) / float64(w.cfg.LogQuotaBytes)
	if ratio >= w.cfg.LogHighWater {
		if w.logHigh.CompareAndSwap(false, true) {
			w.log.Warn("log quota high watermark", "used_bytes", used, "quota_bytes", w.cfg.LogQuotaBytes)
		}
		return
	}
	if ratio <= w.cfg.LogLowWater {
		if w.logHigh.CompareAndSwap(true, false) {
			w.log.Info("log quota recovered; claim resumed", "used_bytes", used)
		}
	}
}

func (w *Worker) logPath(runID uuid.UUID, attempt int) string {
	return filepath.Join(w.cfg.LogRoot, "runs", runID.String(), fmt.Sprintf("attempt-%d.log", attempt))
}

func (w *Worker) ensureLogDir(runID uuid.UUID) error {
	return os.MkdirAll(filepath.Join(w.cfg.LogRoot, "runs", runID.String()), 0o755)
}

func (w *Worker) workspace(runID uuid.UUID) string {
	return filepath.Join(w.cfg.RunWorkspaceRoot, runID.String())
}

func shellCommand(spec domain.JobSpec) string {
	if len(spec.Command) == 1 {
		return spec.Command[0]
	}
	return strings.Join(spec.Command, " ")
}
