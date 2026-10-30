# Control-Plane Performance Check — 2026-10-25

Synthetic local benchmark of the control-plane SQL shape. This is a planning
check, not a production capacity guarantee.

## Environment and dataset

- PostgreSQL 17.11 in a local Docker container (`sched-db`), aarch64 host.
- 1,000 jobs / 1,000 immutable versions.
- 200,000 runs:
  - 100,000 `QUEUED`
  - 50,000 `SUCCEEDED`
  - 25,000 `RUNNING`
  - 25,000 `FAILED`
- 10 required dependency edges and 10 matching `SUCCEEDED` upstream runs with
  the same `time_hour`.
- Reproduce with `make perf`; full output is written to `var/perf/report.txt`.

## Query plans

| Operation | Observed plan / timing |
|---|---|
| Claim batch, 20 runs | `run_claim_idx` index scan + `LockRows`; 0.133 ms execution |
| Complete: load run by id | primary-key index scan; 0.031 ms |
| Complete: update `run_attempt` | unique `(run_id, attempt)` index; 0.122 ms |
| Complete: update `run` | primary-key index scan; 0.180 ms |
| Dependency readiness, 10 required edges | `run_job_time_hour_idx` index-only scan once per edge; 0.080 ms total |
| Cron catch-up insert, 100 runs | 2.606 ms including both FK constraint triggers |

The dependency lookup is covered by `run(job_id, time_hour, state)` and was an
index-only scan in this dataset, so it does not need a table read per edge.

## Claim throughput

`pgbench` with 20 clients, 20 rows per transaction, 200 transactions per client:

```text
transactions processed: 4000/4000
failed transactions:    0
latency average:        9.070 ms
tps:                    2205.108
```

That is about 44k claimed/updated run rows/s in the synthetic setup. The
measured queue had 100k rows, so all 4,000 transactions performed real work.
`FOR UPDATE SKIP LOCKED` kept concurrent claimers from waiting on each other.

## Connection and lock observations

- Application pool: `internal/db/db.go` caps each process at 16 connections.
  `server` and `scheduler` each own a pool; Workers never connect to PostgreSQL.
- Benchmark clients: 20.
- Deadlocks after the claim run: 0. Conflicts: 0.
- Lock waits are bounded by design: claim uses `SKIP LOCKED`; dependency
  readiness serializes only the same `(downstream, time_hour)` via a
  transaction-scoped advisory lock.

## Reading against the target

At the design target (~58 run/s at 10x peak):

- Completion is ~58 transaction/s. Each completion's measured DB work is well
  under 1 ms plus its dependency probes, so this is a small fraction of one
  PostgreSQL core at the measured plan timings.
- Claims batch 20 runs, so claim transactions are ~3/s. Measured batch claim
  throughput is orders of magnitude above that.
- Liveness work is O(Worker owners), not O(runs).
- Cron work is bounded by `SCHED_CRON_CATCHUP_LIMIT` (default 100) per due job
  per tick; one 100-run catch-up inserted in ~3 ms.

## Limits / next evidence

- Results are from a warm cache and a local single node; they do not measure
  production network, storage latency, or 500k/day table growth.
- The synthetic schema has 200k runs; long-run table growth and partitioning are
  P2 work, and `run`/`run_attempt`/`log_manifest` are deliberately not
  partitioned yet.
- Dependency fan-out at the 500-downstream cap and advisory-lock contention at
  very high fan-out were not separately stress-tested. The measured 10-edge case
  is representative of ordinary fan-in, not the worst cap.
- The per-job log archive sweep is not in this benchmark. It runs hourly behind
  a transaction-scoped advisory lock and selects only terminal runs past both
  retention limits using `run_archive_candidate_idx`; its bounded rank
  subquery examines at most the job's retention-count rows per candidate. At
  500k runs/day the long-run cost still needs a dedicated aged-table test.
