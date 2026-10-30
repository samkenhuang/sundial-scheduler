package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
)

func (s *Server) logFile(runID uuid.UUID, attempt int) string {
	return filepath.Join(s.cfg.LogRoot, "central", runID.String(), fmt.Sprintf("attempt-%d.log", attempt))
}

func rejectArchivedLog(w http.ResponseWriter) {
	writeError(w, http.StatusGone, "log_archived", "run log is archived")
}

func (s *Server) handleLogTicket(w http.ResponseWriter, r *http.Request) {
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
	if run.ArchivedAt != nil {
		rejectArchivedLog(w)
		return
	}
	source, _, err := s.logSource(r.Context(), run)
	if err != nil {
		if errors.Is(err, errLogSourceUnavailable) {
			writeError(w, http.StatusConflict, "log_unavailable", "log stream is unavailable: worker is offline or has no local log")
			return
		}
		fail(w, err)
		return
	}
	ticket, expiresIn := s.newLogTicket(id)
	writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "expiresIn": expiresIn, "source": source})
}

func (s *Server) handleGetLog(w http.ResponseWriter, r *http.Request) {
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
	if run.ArchivedAt != nil {
		rejectArchivedLog(w)
		return
	}
	attempt := parseAttempt(r, run.Attempt)
	path := s.logFile(id, attempt)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "log_not_ready", "log is not available yet")
			return
		}
		fail(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("ETag", logETag(info))
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="run-%s-attempt-%d.log"`, id, attempt))
	}
	http.ServeContent(w, r, fmt.Sprintf("run-%s.log", id), info.ModTime(), f)
}

func (s *Server) handleStreamLog(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	if !s.validateLogTicket(id, r.URL.Query().Get("ticket")) {
		writeError(w, http.StatusUnauthorized, "invalid_ticket", "log ticket is missing or expired")
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if run.ArchivedAt != nil {
		rejectArchivedLog(w)
		return
	}
	attempt := parseAttempt(r, run.Attempt)
	path := s.logFile(id, attempt)
	exists := s.centralLogExists(id, attempt)
	offset := lastEventOffset(r)

	// A terminal run with a central copy is the stable snapshot path. Do not
	// involve a Worker unless the cache is missing.
	if run.State.Terminal() && exists {
		s.serveCentralLogStream(w, r, run, attempt, offset)
		return
	}

	follow := !run.State.Terminal()
	if run.State.Terminal() {
		// A recovery replay always starts at byte zero; resuming a partial
		// replay is handled by the next page load rather than risking a
		// misaligned cache file.
		offset = 0
	}
	sub, streamErr := s.startWorkerLogStream(r.Context(), r, run, attempt, follow, offset)
	if streamErr != nil {
		if exists || run.State == domain.RunQueued {
			s.serveCentralLogStream(w, r, run, attempt, offset)
			return
		}
		s.log.Warn("log stream unavailable", "run_id", id, "error", streamErr)
		writeError(w, http.StatusServiceUnavailable, "log_unavailable", "log stream is unavailable: worker is offline or has no local log")
		return
	}
	recoverPath := ""
	if run.State.Terminal() && !exists {
		recoverPath = path + ".recover"
	}
	s.relayWorkerLogStream(w, r, sub, recoverPath)
}

func parseAttempt(r *http.Request, fallback int) int {
	raw := r.URL.Query().Get("attempt")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func logETag(info os.FileInfo) string {
	return fmt.Sprintf(`W/"%x-%x"`, info.Size(), info.ModTime().UnixNano())
}

// worker log ingestion path. The worker remains the local owner; the gateway
// keeps a bounded central copy for the UI. Object archive is P1.
func (s *Server) handleWorkerUploadLog(w http.ResponseWriter, r *http.Request) {
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
	if run.ArchivedAt != nil {
		rejectArchivedLog(w)
		return
	}
	attempt := parseAttempt(r, run.Attempt)
	path := s.logFile(id, attempt)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fail(w, err)
		return
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		fail(w, err)
		return
	}
	const maxLogBytes = 64 << 20
	n, err := io.Copy(f, io.LimitReader(r.Body, maxLogBytes))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(tmp)
		writeError(w, http.StatusBadRequest, "log_write_failed", "could not store log")
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bytes": n, "attempt": attempt})
}
