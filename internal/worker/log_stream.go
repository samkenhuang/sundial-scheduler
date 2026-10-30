package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type logStreamCommand struct {
	RunID        uuid.UUID `json:"runId"`
	Attempt      int       `json:"attempt"`
	Subscription uuid.UUID `json:"subscription"`
	CallbackURL  string    `json:"callbackUrl"`
	ResultURL    string    `json:"resultUrl"`
	FromOffset   int64     `json:"fromOffset"`
	Follow       bool      `json:"follow"`
}

var logStreamClient = &http.Client{}

func (w *Worker) postCommandResult(ctx context.Context, id uuid.UUID, state, message string) {
	body := map[string]any{"state": state}
	if message != "" {
		body["error"] = message
	}
	_, _ = w.cli.do(ctx, "POST", "/worker/v1/commands/"+id.String()+"/result", body, nil)
}

func (w *Worker) runDone(runID uuid.UUID) <-chan struct{} {
	w.mu.Lock()
	ch := w.done[runID]
	w.mu.Unlock()
	if ch != nil {
		return ch
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}

func (w *Worker) startLogStream(parent context.Context, cmd logStreamCommand) error {
	if cmd.RunID == uuid.Nil || cmd.Subscription == uuid.Nil {
		return fmt.Errorf("run and subscription are required")
	}
	if !strings.HasPrefix(cmd.CallbackURL, "http://") && !strings.HasPrefix(cmd.CallbackURL, "https://") {
		return fmt.Errorf("callbackUrl must be an absolute HTTP URL")
	}
	if cmd.CallbackURL == "" {
		return fmt.Errorf("callbackUrl is required")
	}
	if cmd.FromOffset < 0 {
		return fmt.Errorf("fromOffset must be >= 0")
	}
	path := w.logPath(cmd.RunID, cmd.Attempt)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open local log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat local log: %w", err)
	}
	offset := cmd.FromOffset
	if offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		f.Close()
		return fmt.Errorf("seek local log: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)
	w.mu.Lock()
	if old := w.streams[cmd.Subscription]; old != nil {
		old()
	}
	w.streams[cmd.Subscription] = cancel
	w.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			f.Close()
			w.mu.Lock()
			delete(w.streams, cmd.Subscription)
			w.mu.Unlock()
		}()
		w.streamLogFile(ctx, f, offset, cmd)
	}()
	return nil
}

func (w *Worker) streamLogFile(ctx context.Context, f *os.File, fromOffset int64, cmd logStreamCommand) {
	pr, pw := io.Pipe()
	var sent atomic.Int64
	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)
		defer pw.Close()
		buf := make([]byte, 32*1024)
		done := w.runDone(cmd.RunID)
		doneClosed := false
		select {
		case <-done:
			doneClosed = true
		default:
		}
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				if _, writeErr := pw.Write(buf[:n]); writeErr != nil {
					_ = pw.CloseWithError(writeErr)
					return
				}
				sent.Add(int64(n))
			}
			if readErr == nil {
				continue
			}
			if readErr != io.EOF {
				_ = pw.CloseWithError(readErr)
				return
			}
			// A live stream holds the connection open after the current local
			// file EOF until the run's completion signal arrives.
			if !cmd.Follow || doneClosed {
				return
			}
			select {
			case <-done:
				doneClosed = true
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cmd.CallbackURL, pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		<-writerDone
		w.postStreamResult(ctx, cmd, "FAILED", err.Error(), sent.Load())
		return
	}
	req.Header.Set("X-Worker-Token", w.cfg.WorkerToken)
	req.Header.Set("X-Worker-Id", w.id.String())
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	resp, err := logStreamClient.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		<-writerDone
		w.postStreamResult(ctx, cmd, "FAILED", err.Error(), sent.Load())
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	<-writerDone
	if resp.StatusCode >= 300 {
		w.postStreamResult(ctx, cmd, "FAILED", "gateway returned "+strconv.Itoa(resp.StatusCode), sent.Load())
		return
	}
	w.postStreamResult(ctx, cmd, "DONE", "", sent.Load())
}

func (w *Worker) postStreamResult(ctx context.Context, cmd logStreamCommand, state, message string, byteCount int64) {
	if strings.TrimSpace(cmd.ResultURL) == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{
		"state": state, "bytes": byteCount, "error": message,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cmd.ResultURL, bytes.NewReader(body))
	if err != nil {
		w.log.Warn("build log stream result request", "error", err)
		return
	}
	req.Header.Set("X-Worker-Token", w.cfg.WorkerToken)
	req.Header.Set("X-Worker-Id", w.id.String())
	req.Header.Set("Content-Type", "application/json")
	resp, err := logStreamClient.Do(req)
	if err != nil {
		w.log.Warn("post log stream result", "error", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		w.log.Warn("post log stream result rejected", "status", resp.StatusCode)
	}
}
