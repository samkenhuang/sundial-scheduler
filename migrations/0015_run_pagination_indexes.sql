-- Cursor pagination orders visible runs by (created_at DESC, id DESC).
-- Partial indexes keep archived runs out of the page scan.
CREATE INDEX IF NOT EXISTS run_created_id_idx
  ON run (created_at DESC, id DESC) WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS run_state_created_id_idx
  ON run (state, created_at DESC, id DESC) WHERE archived_at IS NULL;
