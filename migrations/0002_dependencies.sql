-- P1: task-level dependencies and all_in_cycle gate state.
-- Additive only; existing P0 rows stay valid with daily cycle defaults.

ALTER TABLE job ADD COLUMN IF NOT EXISTS cycle_format text NOT NULL DEFAULT '2006-01-02';
ALTER TABLE job ADD COLUMN IF NOT EXISTS gate_deadline_seconds int NOT NULL DEFAULT 7200;

CREATE TABLE IF NOT EXISTS job_dependency (
  downstream_job_id uuid NOT NULL REFERENCES job(id) ON DELETE CASCADE,
  upstream_job_id   uuid NOT NULL REFERENCES job(id) ON DELETE CASCADE,
  condition         text NOT NULL DEFAULT 'on_success',
  gate_mode         text NOT NULL DEFAULT 'all_in_cycle',
  required          boolean NOT NULL DEFAULT true,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (downstream_job_id, upstream_job_id),
  CHECK (condition IN ('on_success','always')),
  CHECK (gate_mode IN ('all_in_cycle')),
  CHECK (downstream_job_id <> upstream_job_id)
);
CREATE INDEX IF NOT EXISTS job_dependency_upstream_idx
  ON job_dependency (upstream_job_id);

CREATE TABLE IF NOT EXISTS job_gate (
  downstream_job_id uuid NOT NULL REFERENCES job(id) ON DELETE CASCADE,
  cycle_key         text NOT NULL,
  required_count    int NOT NULL,
  satisfied_count   int NOT NULL DEFAULT 0,
  state             text NOT NULL DEFAULT 'waiting',
  fired_run_id      uuid,
  deadline_at       timestamptz,
  last_error        text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (downstream_job_id, cycle_key),
  CHECK (state IN ('waiting','fired','skipped'))
);
CREATE INDEX IF NOT EXISTS job_gate_waiting_deadline_idx
  ON job_gate (deadline_at) WHERE state = 'waiting';

CREATE TABLE IF NOT EXISTS job_gate_member (
  downstream_job_id uuid NOT NULL,
  cycle_key         text NOT NULL,
  upstream_job_id   uuid NOT NULL,
  run_id            uuid NOT NULL REFERENCES run(id) ON DELETE CASCADE,
  created_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (downstream_job_id, cycle_key, upstream_job_id),
  FOREIGN KEY (downstream_job_id, cycle_key) REFERENCES job_gate(downstream_job_id, cycle_key) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS job_gate_member_run_idx ON job_gate_member (run_id);
