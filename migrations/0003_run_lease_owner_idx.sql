-- Heartbeat and claim paths ask "what is this worker currently running?".
-- Without a lease_owner index those queries scan every RUNNING row (or worse,
-- the whole run table as it grows). A partial index is enough and stays small.
CREATE INDEX IF NOT EXISTS run_lease_owner_idx ON run (lease_owner)
  WHERE state = 'RUNNING';
