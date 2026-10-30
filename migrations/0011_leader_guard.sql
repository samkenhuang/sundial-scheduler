-- Single-row DB guard prevents split-brain between a Redis-lock leader and a
-- PostgreSQL-advisory-lock fallback leader. No epoch; the lease is absorbed by
-- expiry only.
CREATE TABLE IF NOT EXISTS leader_guard (
  id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  owner_id uuid,
  mode text NOT NULL DEFAULT '',
  expires_at timestamptz
);
INSERT INTO leader_guard (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
