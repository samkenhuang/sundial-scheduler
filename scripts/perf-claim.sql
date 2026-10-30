WITH picked AS (
  SELECT id FROM run
  WHERE state='QUEUED' AND available_at <= clock_timestamp()
    AND required_labels <@ '["linux"]'::jsonb
  ORDER BY priority DESC, available_at
  LIMIT 20
  FOR UPDATE SKIP LOCKED
)
UPDATE run SET state='RUNNING', lease_owner='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'::uuid,
  lease_token=gen_random_uuid(),
  started_at=clock_timestamp(), version=version+1
FROM picked WHERE run.id=picked.id;
