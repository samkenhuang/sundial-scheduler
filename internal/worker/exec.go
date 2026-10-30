package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
)

type completion struct {
	RunID      uuid.UUID `json:"runId"`
	WorkerID   uuid.UUID `json:"workerId"`
	LeaseToken uuid.UUID `json:"leaseToken"`
	Attempt    int       `json:"attempt"`
	ExitCode   *int      `json:"exitCode"`
	TimedOut   bool      `json:"timedOut"`
	Error      string    `json:"error,omitempty"`
}

func (w *Worker) execute(parent context.Context, run claimedRun) {
	runID, leaseToken, timeHour, attempt, spec := run.RunID, run.LeaseToken, run.TimeHour, run.Attempt, run.Spec
	spec.Normalize()
	ctx, cancel := context.WithTimeout(parent, spec.Timeout())
	defer cancel()

	w.mu.Lock()
	if old := w.running[runID]; old != nil {
		old()
	}
	w.running[runID] = cancel
	done := make(chan struct{})
	w.done[runID] = done
	w.mu.Unlock()
	defer func() {
		close(done)
		w.mu.Lock()
		delete(w.running, runID)
		delete(w.done, runID)
		w.mu.Unlock()
	}()

	if err := w.ensureLogDir(runID); err != nil {
		w.log.Error("create log dir", "run_id", runID, "error", err)
		return
	}
	logPath := w.logPath(runID, attempt)
	initialSize := int64(0)
	if info, statErr := os.Stat(logPath); statErr == nil {
		initialSize = info.Size()
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		w.log.Error("open log file", "run_id", runID, "error", err)
		return
	}
	defer logFile.Close()

	// The header is only written for a fresh log. There is no retry/requeue, but
	// keeping this guard makes recovery/restart paths idempotent.
	if initialSize == 0 {
		if _, err := logFile.WriteString(formatBuildLogHeader(w.id, w.host, run) + "\n"); err != nil {
			w.log.Warn("write log header", "run_id", runID, "error", err)
		}
	}

	workDir := w.workspace(runID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		w.log.Error("create workspace", "run_id", runID, "error", err)
		return
	}
	if spec.Cwd != "" {
		workDir = filepath.Join(workDir, spec.Cwd)
		_ = os.MkdirAll(workDir, 0o755)
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", shellCommand(spec))
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Kill the whole process group so grandchildren do not outlive a
		// canceled or timed-out run.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "SCHED_RUN_ID="+runID.String(), "SCHED_ATTEMPT="+strconv.Itoa(attempt),
		"TIME_HOUR="+timeHour)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		w.complete(runID, leaseToken, attempt, nil, false, fmt.Sprintf("start command: %v", err))
		return
	}

	go w.uploadLogLoop(ctx, runID, attempt)

	waitErr := cmd.Wait()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	exitCode := cmd.ProcessState.ExitCode()
	if exitCode < 0 {
		exitCode = 1
	}
	errText := ""
	if waitErr != nil && !timedOut {
		errText = waitErr.Error()
	}
	if timedOut {
		errText = "run timed out"
	}
	_ = logFile.Sync()
	w.trackLogFile(logPath, initialSize)
	w.uploadLog(context.Background(), runID, attempt)
	w.complete(runID, leaseToken, attempt, &exitCode, timedOut, errText)
	// Publish log usage without waiting for the next 30s heartbeat. Bound it so
	// a control-plane outage cannot hold this worker slot.
	hbCtx, hbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	w.heartbeat(hbCtx)
	hbCancel()
}

func (w *Worker) complete(runID, leaseToken uuid.UUID, attempt int, exitCode *int, timedOut bool, errText string) {
	c := completion{
		RunID: runID, WorkerID: w.id, LeaseToken: leaseToken, Attempt: attempt,
		ExitCode: exitCode, TimedOut: timedOut, Error: errText,
	}
	if w.postCompletion(context.Background(), c) {
		return
	}
	w.spoolCompletion(c)
}

func (w *Worker) postCompletion(ctx context.Context, c completion) bool {
	body := map[string]any{
		"workerId": c.WorkerID, "leaseToken": c.LeaseToken, "exitCode": c.ExitCode,
		"timedOut": c.TimedOut, "error": c.Error,
	}
	for attempt := 0; attempt < 3; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		status, err := w.cli.do(reqCtx, "POST", "/worker/v1/runs/"+c.RunID.String()+"/complete", body, nil)
		cancel()
		if err == nil && status < 300 {
			return true
		}
		if status == 409 || status == 410 {
			return true
		}
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}
	return false
}

func (w *Worker) spoolCompletion(c completion) {
	dir := filepath.Join(w.cfg.SpoolRoot, "completions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, c.RunID.String()+"-"+strconv.Itoa(c.Attempt)+".json"), b, 0o644)
}

func (w *Worker) flushSpool(ctx context.Context) {
	dir := filepath.Join(w.cfg.SpoolRoot, "completions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var c completion
		if err := json.Unmarshal(b, &c); err != nil {
			_ = os.Remove(path)
			continue
		}
		if w.postCompletion(ctx, c) {
			_ = w.uploadLog(ctx, c.RunID, c.Attempt)
			_ = os.Remove(path)
		}
	}
}

func (w *Worker) uploadLogLoop(ctx context.Context, runID uuid.UUID, attempt int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.uploadLog(ctx, runID, attempt)
		}
	}
}

func (w *Worker) uploadLog(ctx context.Context, runID uuid.UUID, attempt int) error {
	path := w.logPath(runID, attempt)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	status, err := w.cli.uploadFile(reqCtx, "/worker/v1/runs/"+runID.String()+"/log?attempt="+strconv.Itoa(attempt), f)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("upload log status %d", status)
	}
	return nil
}
