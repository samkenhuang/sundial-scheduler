# P1: 任务依赖与 all_in_cycle 周期门控

> **Status: superseded.** This plan records an earlier design and is not an implementation authority. The canonical plan is [`2026-10-25-scheduler-platform-development-outline.md`](2026-10-25-scheduler-platform-development-outline.md).


## Goal

在 P0 闭环上实现 DESIGN.md §7 的 MVP 依赖语义：任务级有向依赖、`all_in_cycle`
周期门控、上游终态推进门控、下游每个 cycle 只触发一次、必需上游失败跳过、
门控超时跳过；依赖关系通过任务详情 API 读写，不新增独立依赖实体页面。

## Architecture / Authority

- Authority: `DESIGN.md` §7.2/§7.4, §6.2 run state machine, P0 migration.
- Postgres remains source of truth. Gate advancement happens in the same transaction
  that makes an upstream run terminal; `run.dependencies_processed_at` is the
  idempotency guard.
- Existing owners: `internal/store/runs.go` completion/cancel/timeout; new
  `internal/store/dependencies.go` owns edges/gates; `internal/api/jobs.go` exposes
  dependency CRUD on the job detail; scheduler expires waiting gates.
- No queue/trigger entity; launch mode `dependency` is a normal run field.
- Single tenant; dependency edges are not project-scoped.
- `per_run` gate mode remains reserved and rejected at the API in P1.

## Compatibility Boundary

- Existing P0 APIs/runs stay valid. `cycleKey` on trigger was already accepted;
  cron runs will now populate it using `job.cycle_format` (default daily).
- New job columns get defaults; existing jobs are valid. Existing P0 runs with
  null `cycle_key` are not gated retroactively.
- New migration `0002_dependencies.sql` is additive and idempotent under the
  existing migration runner.
- No dependency UI/page; `GET/PUT /v1/jobs/{id}/dependencies` and gate state are
  sufficient for a future job-detail page.

## TDD Route

- Mode: off.
- Decision: skipped.
- Authority: Aegis TDD mode off; no explicit strict TDD request.
- Posture: minimum implementation plus focused store/API integration coverage and
  an end-to-end dependency scenario in `scripts/integration.sh`.

## Change Necessity

Current code cannot express upstream edges or atomically fire a downstream run
after all required upstreams succeed. A schema plus gate transaction is the
minimum owner change; an API-only or worker-side solution would make dependency
state non-durable and violate the at-least-once/once-per-cycle contract.

## Requirement Ready Check

- Source: DESIGN.md §7.2 and §7.4.
- Scenarios: success fan-in, duplicate upstream within cycle, required failure
  skip, non-required failure ignored, cycle isolation, replay/backfill via
  explicit `cycleKey`, cron cycle key, gate expiry.
- Acceptance evidence: migration applies; cycle validation rejects cycles; one
  downstream run per `(job, cycle)`; skipped gate on required failure; integration
  script creates A/B -> D and observes exactly one D run; failover tests still pass.

## Ripple Signal Triage

- Persistence/schema, public API contract, scheduler→run producer, terminal run
  consumer. Canonical owner is Postgres gate transaction; API and scheduler are
  thin adapters. No duplicate owners or fallback path.
- Expanded verification: migration on fresh DB, store integration for success/
  duplicate/failure/cycle isolation, HTTP integration through real worker, cron
  regression, failover unaffected.

## Plan Pressure Test

- Owner fit: gate advancement must live with terminal run update, not worker.
- Architecture: no master-to-master calls; Postgres row locks/CAS only.
- Verification: real HTTP+worker scenario, not just unit tests.
- Executability: six bounded tasks with exact files.

## Tasks

### Task 1. Schema + cycle model
Files: `migrations/0002_dependencies.sql`, `internal/domain/models.go`,
`internal/domain/spec.go` (or new `cycle.go`), `internal/store/jobs.go`.
- Add job `cycle_format` (default `2006-01-02`) and `gate_deadline_seconds`
  (default 7200); add `job_dependency`, `job_gate`, `job_gate_member`.
- Add `domain.CycleKey(format, tz, at)` and `domain.CycleFormat` validation
  (day/month/week/year supported layouts); expose `cycleFormat` on job APIs.
- Keep `per_run` rejected.
Verify: migration fresh+twice, domain tests for cycle truncation/timezone.

### Task 2. Dependency read/write + graph constraints
Files: `internal/store/dependencies.go`, `internal/api/dependencies.go`,
`internal/api/server.go`, `internal/api/jobs.go`.
- `PUT /v1/jobs/{id}/dependencies` replaces upstream edges atomically.
- `GET /v1/jobs/{id}/dependencies` returns upstream/downstream names + gate mode.
- Validate self-edge, job existence, max fan-out 500, cycle-free via recursive SQL,
  max depth 100; audit changes; reject `per_run` in P1.
- Response includes `cycleFormat`/`gateDeadlineSeconds` on job detail.
Verify: API cycle rejection, self-edge, duplicate replace, auth.

### Task 3. Cycle key on run creation
Files: `internal/store/runs.go`, `internal/store/scheduler.go`,
`internal/api/jobs.go`, `internal/domain/cycle.go`.
- Trigger API: explicit `cycleKey` wins; else compute from job.cycle_format.
- Cron `FireDueJob`: set `cycle_key` from dueAt + job cycle_format/timezone.
- Preserve existing idempotency/dedup semantics.
Verify: store test for trigger cycle, integration cron run has cycleKey.

### Task 4. Terminal-run gate advancement (core)
Files: `internal/store/runs.go`, `internal/store/dependencies.go`,
`internal/store/scheduler.go`.
- Add `advanceDependencies(ctx, tx, run)` called in `CompleteRun` for terminal
  states, `CancelRun` queued terminal, `TimeoutRuns` (per selected run), and
  `ReapExpiredRuns` BLOCKED terminal.
- Ensure gate row; member insert `ON CONFLICT DO NOTHING`; only required edges
  count; on_success failure of required edge → gate `skipped` (unless condition
  always); on success count; when count reaches required_count and state waiting,
  create downstream run with `launch_mode='dependency'`, same cycle_key,
  `dedup_key='dep:'+job+':'+cycle`, current version; mark gate fired.
- Set `run.dependencies_processed_at` exactly once in the same transaction.
Verify: store integration success fan-in, duplicate member, required failure,
non-required failure, already-fired dedup.

### Task 5. Gate expiry + scheduler loop
Files: `internal/store/dependencies.go`, `internal/scheduler/scheduler.go`.
- `ExpireWaitingGates(ctx, limit)` with `SKIP LOCKED`; state `skipped`,
  `updated_at`, error reason; do not create runs.
- Scheduler calls it once per work tick (global, multiple masters compete).
- Add gate deadline default 2h from gate creation; expose deadline in response.
Verify: store test with past deadline; integration unaffected.

### Task 6. End-to-end + docs
Files: `scripts/integration.sh`, `README.md`, P0/P1 plan status.
- Add dependency scenario: A/B upstream echo jobs, D downstream; PUT edges;
  trigger A and B with same explicit `cycleKey`; poll exactly one D run
  `launch_mode=dependency`, same cycle; verify its worker log.
- Add failure gate scenario: upstream exit 1 required -> no downstream run;
  `GET dependencies`/gates shows skipped.
- Keep retry/timeout/cancel/cron/outage/failover green.
- Document API and P1 limitations (per_run reserved).

## Verification Commands

```bash
go build ./... && go vet ./... && go test ./...
TEST_DATABASE_URL='postgres://sched:sched@127.0.0.1:55432/sched_test?sslmode=disable' go test ./internal/store -v
./scripts/integration.sh
./scripts/failover.sh
```

## Risks / Uncovered

- Gate advancement adds work to the terminal transaction; bounded by fan-out 500.
- No `per_run` fan-out, no reset/backfill UI, no gate alert/outbox yet.
- `cycle_format` supports Go layouts; only day/month/week/year are validated.
- No partitioning; gate tables should be partition-friendly later.
- No dedicated dependency page; job detail endpoint only.

## Execution Route

`inline`. Schema + terminal transaction + API + scheduler are tightly coupled and
share files; no independent parallel task ownership. Each task gets a focused
test; final run re-runs P0 integration/failover.

## Implementation Status (2026-10-24)

Implemented and verified inline:

- `migrations/0002_dependencies.sql`: job cycle fields, `job_dependency`,
  `job_gate`, `job_gate_member`, indexes.
- `internal/domain/cycle.go`: timezone-aware daily/monthly/yearly/hourly
  `CycleKey` + format validation.
- API: `GET/PUT /v1/jobs/{id}/dependencies`, `GET /v1/jobs/{id}/gates`,
  `GET /v1/jobs/{id}/gates/by-cycle?cycleKey=...`; job create accepts
  `cycleFormat` / `gateDeadlineSeconds`; trigger defaults `cycleKey`; cron
  populates it.
- Store: graph validation (self, duplicate, cycle, depth 100, fanout 500),
  terminal-run gate advancement in the same transaction, gate member dedup,
  exactly one `launch_mode=dependency` run per `(job, cycle)`, required failure
  skip, non-required ignore, deadline expiry via `ExpireWaitingGates`.
- Scheduler: expires waiting gates once per work tick; failover unaffected.
- Verification actually run:
  - `TEST_DATABASE_URL=... go test ./internal/store -run TestDependency -v`: PASS
    (fan-in once, required failure skip, non-required failure ignored, cycle
    isolation, cycle rejection, deadline skip).
  - `scripts/integration.sh`: PASS including dependency success and required
    failure scenarios, cron cycle keys, retry/timeout/cancel/outage.
  - `scripts/failover.sh`: PASS.
- Remaining: per_run reserved, gate reset/backfill UI, gate alert/outbox, gate
  deadline is creation+2h rather than cycle-end+2h, edge edits do not rewrite a
  waiting gate.
