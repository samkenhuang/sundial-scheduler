package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
)

type archiveResult struct {
	Archived     int  `json:"archived"`
	FilesMoved   int  `json:"filesMoved"`
	FilesMissing int  `json:"filesMissing"`
	MoveErrors   int  `json:"moveErrors"`
	Busy         bool `json:"busy"`
}

func (s *Server) archiveLogFile(runID uuid.UUID, attempt int) string {
	return filepath.Join(s.cfg.LogRoot, "archive", runID.String(), "attempt-"+strconv.Itoa(attempt)+".log")
}

// archiveOnce claims one retention batch in the database, then moves the
// corresponding central log files. The DB mark lands first: even if a file
// move fails, log endpoints refuse archived runs and the next sweep will not
// select them again. That is safer than exposing a central file whose run was
// already declared archived.
func (s *Server) archiveOnce(ctx context.Context, limit int) (archiveResult, error) {
	var res archiveResult
	candidates, locked, err := s.store.ClaimRunsForArchive(ctx, limit)
	if err != nil {
		return res, err
	}
	res.Busy = !locked
	res.Archived = len(candidates)
	for _, c := range candidates {
		src := s.logFile(c.RunID, c.Attempt)
		dst := s.archiveLogFile(c.RunID, c.Attempt)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			res.MoveErrors++
			s.log.Error("create archive directory", "run_id", c.RunID, "error", err)
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				res.FilesMissing++
				continue
			}
			res.MoveErrors++
			s.log.Error("move log to archive", "run_id", c.RunID, "error", err)
			continue
		}
		res.FilesMoved++
	}
	if res.Archived > 0 || res.MoveErrors > 0 {
		s.log.Info("log archive sweep", "archived", res.Archived, "moved", res.FilesMoved,
			"missing", res.FilesMissing, "move_errors", res.MoveErrors)
	}
	return res, nil
}

func (s *Server) logArchiveLoop(ctx context.Context) {
	if s.cfg.LogArchiveInterval <= 0 {
		return
	}
	if _, err := s.archiveOnce(ctx, s.cfg.LogArchiveBatch); err != nil {
		s.log.Error("initial log archive sweep failed", "error", err)
	}
	ticker := time.NewTicker(s.cfg.LogArchiveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.archiveOnce(ctx, s.cfg.LogArchiveBatch); err != nil {
				s.log.Error("log archive sweep failed", "error", err)
			}
		}
	}
}

func (s *Server) handleAdminLogArchive(w http.ResponseWriter, r *http.Request) {
	res, err := s.archiveOnce(r.Context(), s.cfg.LogArchiveBatch)
	if err != nil {
		fail(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "log.archive", "run", "",
		nil, res)
	writeJSON(w, http.StatusOK, res)
}
