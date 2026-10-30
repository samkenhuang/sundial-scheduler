-- Executors are manageable first-class records now: the admin can pre-create
-- one, name it, and hand its ID to the Worker process. Existing auto-registered
-- workers keep their hostname as the initial display name.
ALTER TABLE worker ADD COLUMN IF NOT EXISTS name text;
ALTER TABLE worker ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '';

UPDATE worker
SET name = COALESCE(NULLIF(trim(hostname), ''), 'executor-' || left(id::text, 8))
WHERE name IS NULL OR trim(name) = '';

-- Hostnames are not unique (several workers can run on one host), while the
-- executor name is a management identity. Disambiguate only the historical
-- duplicates, then enforce the invariant for all future writes.
WITH ranked AS (
  SELECT id, name, row_number() OVER (PARTITION BY lower(name) ORDER BY registered_at, id) AS pos
  FROM worker
)
UPDATE worker w
SET name = w.name || '-' || left(w.id::text, 8)
FROM ranked r
WHERE w.id = r.id AND r.pos > 1;

ALTER TABLE worker ALTER COLUMN name SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS worker_name_lower_uq ON worker (lower(name));
