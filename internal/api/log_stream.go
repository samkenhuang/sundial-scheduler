package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
	"scheduler-platform/internal/store"
)

var (
	errLogSourceUnavailable = errors.New("log source is unavailable")
	errLogStreamTimeout     = errors.New("worker log stream did not connect")
)

type logStreamSubscription struct {
	ID             uuid.UUID
	RunID          uuid.UUID
	Attempt        int
	ExpectedWorker uuid.UUID
	Follow         bool
	FromOffset     int64
	Reader         *io.PipeReader
	Writer         *io.PipeWriter

	connected chan struct{}
	complete  chan struct{}

	mu           sync.Mutex
	connectedSet bool
	completeSet  bool
	state        string
	streamErr    string
	bytes        int64
}

func newLogStreamSubscription(runID uuid.UUID, attempt int, worker uuid.UUID, follow bool, fromOffset int64) *logStreamSubscription {
	reader, writer := io.Pipe()
	return &logStreamSubscription{
		ID: uuid.New(), RunID: runID, Attempt: attempt, ExpectedWorker: worker,
		Follow: follow, FromOffset: fromOffset,
		Reader: reader, Writer: writer,
		connected: make(chan struct{}), complete: make(chan struct{}),
	}
}

func (s *logStreamSubscription) markConnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connectedSet {
		return
	}
	s.connectedSet = true
	close(s.connected)
}

func (s *logStreamSubscription) markComplete(state, message string, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completeSet {
		return
	}
	s.completeSet = true
	s.state = state
	s.streamErr = message
	s.bytes = bytes
	close(s.complete)
}

func (s *logStreamSubscription) connectedCh() <-chan struct{} { return s.connected }

func (s *logStreamSubscription) completeCh() <-chan struct{} { return s.complete }

func (s *logStreamSubscription) result() (state, message string, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.streamErr, s.bytes
}

func (s *logStreamSubscription) close() {
	_ = s.Reader.CloseWithError(errLogStreamTimeout)
	_ = s.Writer.CloseWithError(errLogStreamTimeout)
}

type logStreamHub struct {
	mu sync.Mutex
	m  map[uuid.UUID]*logStreamSubscription
}

func newLogStreamHub() *logStreamHub {
	return &logStreamHub{m: map[uuid.UUID]*logStreamSubscription{}}
}

func (h *logStreamHub) add(runID uuid.UUID, attempt int, worker uuid.UUID, follow bool, fromOffset int64) *logStreamSubscription {
	sub := newLogStreamSubscription(runID, attempt, worker, follow, fromOffset)
	h.mu.Lock()
	h.m[sub.ID] = sub
	h.mu.Unlock()
	return sub
}

func (h *logStreamHub) get(id uuid.UUID) *logStreamSubscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.m[id]
}

func (h *logStreamHub) remove(id uuid.UUID) {
	h.mu.Lock()
	delete(h.m, id)
	h.mu.Unlock()
}

func (s *Server) logStreamCallbackBase(r *http.Request) string {
	if s.cfg.LogStreamCallbackURL != "" {
		return strings.TrimRight(s.cfg.LogStreamCallbackURL, "/")
	}
	host := s.cfg.WorkerAddr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	host = strings.Replace(host, "0.0.0.0:", "127.0.0.1:", 1)
	host = strings.Replace(host, "[::]:", "127.0.0.1:", 1)
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}
	return "http://" + strings.TrimRight(host, "/")
}

func (s *Server) logStreamCallbackURL(r *http.Request, subID uuid.UUID) string {
	return s.logStreamCallbackBase(r) + "/worker/v1/log-streams/" + subID.String()
}

func (s *Server) logSource(ctx context.Context, run domain.Run) (string, uuid.UUID, error) {
	centralExists := s.centralLogExists(run.ID, run.Attempt)
	workerID, hasWorker, err := s.store.LatestRunWorker(ctx, run.ID, run.Attempt)
	if err != nil {
		return "", uuid.Nil, err
	}
	online := false
	if hasWorker {
		online = s.workerOnline(ctx, workerID)
	}
	if run.State.Terminal() {
		if centralExists {
			return "central", workerID, nil
		}
		if online {
			return "worker", workerID, nil
		}
		return "", workerID, errLogSourceUnavailable
	}
	if online {
		return "worker", workerID, nil
	}
	// A queued run has no Worker yet. The console may open its (empty) log
	// panel; the central waiter will start tailing once a Worker uploads.
	if run.State == domain.RunQueued {
		return "central", workerID, nil
	}
	if centralExists {
		return "central", workerID, nil
	}
	return "", workerID, errLogSourceUnavailable
}

func (s *Server) workerOnline(ctx context.Context, workerID uuid.UUID) bool {
	worker, err := s.store.GetWorker(ctx, workerID)
	if err != nil {
		return false
	}
	workers := []domain.Worker{worker}
	s.bindPresence(ctx, workers)
	return workers[0].Status == "ONLINE"
}

func (s *Server) centralLogExists(runID uuid.UUID, attempt int) bool {
	info, err := os.Stat(s.logFile(runID, attempt))
	return err == nil && !info.IsDir()
}

func (s *Server) startWorkerLogStream(ctx context.Context, r *http.Request, run domain.Run, attempt int, follow bool, fromOffset int64) (*logStreamSubscription, error) {
	workerID, ok, err := s.store.LatestRunWorker(ctx, run.ID, attempt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errLogSourceUnavailable
	}
	if !s.workerOnline(ctx, workerID) {
		return nil, errLogSourceUnavailable
	}
	sub := s.logStreams.add(run.ID, attempt, workerID, follow, fromOffset)
	callbackURL := s.logStreamCallbackURL(r, sub.ID)
	_, err = s.store.EnqueueCommand(ctx, store.EnqueueCommandInput{
		WorkerID: workerID,
		RunID:    &run.ID,
		Attempt:  &attempt,
		Kind:     "start_log_stream",
		Payload: map[string]any{
			"runId":        run.ID.String(),
			"attempt":      attempt,
			"subscription": sub.ID.String(),
			"callbackUrl":  callbackURL,
			"resultUrl":    callbackURL + "/result",
			"fromOffset":   fromOffset,
			"follow":       follow,
		},
		TTL: 2 * time.Minute,
	})
	if err != nil {
		s.logStreams.remove(sub.ID)
		sub.close()
		return nil, err
	}
	select {
	case <-sub.connectedCh():
		return sub, nil
	case <-time.After(4 * time.Second):
		s.logStreams.remove(sub.ID)
		sub.close()
		return nil, errLogStreamTimeout
	case <-ctx.Done():
		s.logStreams.remove(sub.ID)
		sub.close()
		return nil, ctx.Err()
	}
}

func (s *Server) handleWorkerLogStream(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	sub := s.logStreams.get(id)
	if sub == nil {
		writeError(w, http.StatusNotFound, "log_stream_not_found", "log stream subscription not found")
		return
	}
	workerID := strings.TrimSpace(r.Header.Get("X-Worker-Id"))
	if workerID == "" || workerID != sub.ExpectedWorker.String() {
		writeError(w, http.StatusForbidden, "invalid_worker", "log stream does not belong to this worker")
		return
	}
	sub.markConnected()
	// Do not write response headers until the body is fully consumed: the
	// worker client waits for this response before posting the completion
	// marker. If the browser goes away, the pipe returns an error and the
	// worker sees a failed upload instead of a successful completion.
	_, copyErr := io.Copy(sub.Writer, r.Body)
	_ = sub.Writer.CloseWithError(copyErr)
	if copyErr != nil {
		writeError(w, http.StatusBadGateway, "log_stream_write_failed", "log stream was interrupted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type workerLogStreamResult struct {
	State string `json:"state"`
	Bytes int64  `json:"bytes"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleWorkerLogStreamResult(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	sub := s.logStreams.get(id)
	if sub == nil {
		writeError(w, http.StatusNotFound, "log_stream_not_found", "log stream subscription not found")
		return
	}
	workerID := strings.TrimSpace(r.Header.Get("X-Worker-Id"))
	if workerID == "" || workerID != sub.ExpectedWorker.String() {
		writeError(w, http.StatusForbidden, "invalid_worker", "log stream does not belong to this worker")
		return
	}
	var req workerLogStreamResult
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.State != "DONE" && req.State != "FAILED" {
		writeError(w, http.StatusBadRequest, "invalid_state", "state must be DONE or FAILED")
		return
	}
	sub.markComplete(req.State, strings.TrimSpace(req.Error), req.Bytes)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) awaitLogStreamComplete(ctx context.Context, sub *logStreamSubscription, timeout time.Duration) (string, string, int64, error) {
	select {
	case <-sub.completeCh():
		state, message, bytes := sub.result()
		return state, message, bytes, nil
	case <-time.After(timeout):
		return "", "", 0, errors.New("worker did not report log stream completion")
	case <-ctx.Done():
		return "", "", 0, ctx.Err()
	}
}

func parentLogDir(path string) string { return filepath.Dir(path) }

// sseWriter centralizes line framing and byte-offset IDs for resumable streams.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming unsupported")
	}
	return &sseWriter{w: w, flusher: flusher}, nil
}

func (s *sseWriter) headers() {
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.flusher.Flush()
}

func (s *sseWriter) source(source string) {
	fmt.Fprintf(s.w, "event: source\ndata: %s\n\n", source)
	s.flusher.Flush()
}

func (s *sseWriter) line(offset int64, line string) {
	fmt.Fprintf(s.w, "id: %d\ndata: %s\n\n", offset, strings.TrimRight(line, "\r"))
	s.flusher.Flush()
}

func (s *sseWriter) done() {
	fmt.Fprint(s.w, "event: done\ndata: {}\n\n")
	s.flusher.Flush()
}

func (s *sseWriter) failed(message string) {
	fmt.Fprintf(s.w, "event: failed\ndata: %s\n\n", strings.ReplaceAll(message, "\n", " "))
	s.flusher.Flush()
}

func lastEventOffset(r *http.Request) int64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (s *Server) relayWorkerLogStream(w http.ResponseWriter, r *http.Request, sub *logStreamSubscription, recoverPath string) {
	sse, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", err.Error())
		return
	}
	defer func() {
		s.logStreams.remove(sub.ID)
		sub.close()
	}()
	sse.headers()
	sse.source("worker")

	var tmp *os.File
	ok := false
	if recoverPath != "" {
		if err := os.MkdirAll(parentLogDir(recoverPath), 0o755); err != nil {
			sse.failed("无法创建日志缓存目录。")
			return
		}
		tmp, err = os.OpenFile(recoverPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			sse.failed("无法创建日志缓存文件。")
			return
		}
		defer func() {
			if !ok {
				_ = os.Remove(recoverPath)
			}
		}()
	}

	var reader io.Reader = sub.Reader
	if tmp != nil {
		reader = io.TeeReader(reader, tmp)
	}
	buffered := bufio.NewReaderSize(reader, 64*1024)
	offset := sub.FromOffset
	for {
		line, readErr := buffered.ReadString('\n')
		if len(line) > 0 {
			offset += int64(len(line))
			sse.line(offset, strings.TrimSuffix(line, "\n"))
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			sse.failed("日志流读取失败。")
			return
		}
	}
	state, message, _, completeErr := s.awaitLogStreamComplete(r.Context(), sub, 2*time.Second)
	if completeErr != nil || state != "DONE" {
		if message == "" {
			message = "Worker 未确认日志流完整结束。"
		}
		sse.failed(message)
		return
	}
	if tmp != nil {
		if err := tmp.Sync(); err != nil {
			sse.failed("无法写入日志缓存。")
			return
		}
		if err := tmp.Close(); err != nil {
			sse.failed("无法关闭日志缓存。")
			return
		}
		if err := os.Rename(recoverPath, s.logFile(sub.RunID, sub.Attempt)); err != nil {
			sse.failed("无法保存日志缓存。")
			return
		}
		ok = true
	}
	sse.done()
}

func (s *Server) serveCentralLogStream(w http.ResponseWriter, r *http.Request, run domain.Run, attempt int, fromOffset int64) {
	sse, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", err.Error())
		return
	}
	sse.headers()
	sse.source("central")
	path := s.logFile(run.ID, attempt)
	offset := fromOffset
	idle := time.NewTimer(5 * time.Second)
	defer idle.Stop()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			f, err := os.Open(path)
			if err != nil {
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(5 * time.Second)
			} else {
				if _, err := f.Seek(offset, io.SeekStart); err == nil {
					reader := bufio.NewReader(f)
					for {
						line, readErr := reader.ReadString('\n')
						if len(line) > 0 {
							offset += int64(len(line))
							sse.line(offset, strings.TrimSuffix(line, "\n"))
						}
						if readErr != nil {
							break
						}
					}
					info, statErr := f.Stat()
					if statErr == nil && offset >= info.Size() {
						if current, getErr := s.store.GetRun(r.Context(), run.ID); getErr == nil && current.State.Terminal() {
							f.Close()
							sse.done()
							return
						}
					}
				}
				f.Close()
			}
		case <-idle.C:
			if current, err := s.store.GetRun(r.Context(), run.ID); err == nil && current.State.Terminal() {
				sse.done()
				return
			}
			idle.Reset(5 * time.Second)
		}
	}
}
