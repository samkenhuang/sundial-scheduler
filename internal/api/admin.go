package api

import (
	"net/http"
	"time"

	"scheduler-platform/internal/store"
)

type logCleanupRequest struct {
	Before string `json:"before,omitempty"`
}

// handleAdminLogCleanup fans a log_cleanup command out to every known worker.
// Workers keep their own local files; the control plane never deletes worker
// disk directly.
func (s *Server) handleAdminLogCleanup(w http.ResponseWriter, r *http.Request) {
	var req logCleanupRequest
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	before := time.Now().Add(-s.cfg.LogRetention)
	if req.Before != "" {
		t, err := time.Parse(time.RFC3339, req.Before)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_before", "before must be RFC3339")
			return
		}
		before = t
	}
	workers, err := s.store.ListWorkers(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	queued := 0
	for _, worker := range workers {
		if _, err := s.store.EnqueueCommand(r.Context(), store.EnqueueCommandInput{
			WorkerID: worker.ID, Kind: "log_cleanup",
			Payload: map[string]string{"before": before.UTC().Format(time.RFC3339)},
			TTL:     2 * time.Minute,
		}); err != nil {
			fail(w, err)
			return
		}
		queued++
	}
	_ = s.store.Audit(r.Context(), actorFromContext(r.Context()), "worker.log_cleanup", "worker", "",
		nil, map[string]any{"queued": queued, "before": before.UTC()})
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued, "before": before.UTC()})
}
