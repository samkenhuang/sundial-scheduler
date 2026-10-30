package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"scheduler-platform/internal/db"
	"scheduler-platform/internal/domain"
)

func testStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	if err := db.Migrate(ctx, url, "../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `TRUNCATE job, project, job_version, run, run_attempt, worker,
		worker_command, log_manifest, sched_node, cluster_meta, leader_guard,
		event_outbox, audit_log, idempotency_key RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	st := New(pool)
	return st, ctx
}

func TestJobNamesBatches(t *testing.T) {
	st, ctx := testStore(t)
	first, err := st.CreateJob(ctx, CreateJobInput{Name: "names-" + uuid.NewString(), CreatedBy: "test"})
	if err != nil {
		t.Fatalf("create first job: %v", err)
	}
	second, err := st.CreateJob(ctx, CreateJobInput{Name: "names-" + uuid.NewString(), CreatedBy: "test"})
	if err != nil {
		t.Fatalf("create second job: %v", err)
	}
	missing := uuid.New()
	names, err := st.JobNames(ctx, []uuid.UUID{first.ID, second.ID, missing})
	if err != nil {
		t.Fatalf("job names: %v", err)
	}
	if len(names) != 2 || names[first.ID] != first.Name || names[second.ID] != second.Name {
		t.Fatalf("unexpected names: %#v", names)
	}
	if _, ok := names[missing]; ok {
		t.Fatalf("missing job should not resolve")
	}
	empty, err := st.JobNames(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty ids: %v %#v", err, empty)
	}
}

func mkJobRun(t *testing.T, st *Store, ctx context.Context) (domain.Job, domain.JobVersion, domain.Run, uuid.UUID) {
	t.Helper()
	job, err := st.CreateJob(ctx, CreateJobInput{Name: "test-" + uuid.NewString(), CreatedBy: "test"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	spec := domain.JobSpec{Command: []string{"echo hi"}, Labels: []string{"linux"}}
	spec.Normalize()
	hash, _ := spec.Hash()
	version, err := st.PublishJobVersion(ctx, job.ID, spec, hash, "test")
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	workerID := uuid.New()
	if _, err := st.RegisterWorker(ctx, RegisterWorkerInput{ID: workerID, Labels: []string{"linux"}, ConcurrencyLimit: 2}); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	run, err := st.CreateRun(ctx, CreateRunInput{
		JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api",
		Spec: spec, Priority: 1,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return job, version, run.Run, workerID
}

func TestLatestRunsByJob(t *testing.T) {
	st, ctx := testStore(t)
	a, av, as := mkJobVersion(t, st, ctx, "latest-a")
	b, bv, bs := mkJobVersion(t, st, ctx, "latest-b")
	r1, err := st.CreateRun(ctx, CreateRunInput{JobID: a.ID, JobVersionID: av.ID, LaunchMode: "api", Spec: as, TimeHour: "202610010101"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE run SET created_at=now()-interval '1 hour' WHERE id=$1`, r1.Run.ID); err != nil {
		t.Fatal(err)
	}
	r2, err := st.CreateRun(ctx, CreateRunInput{JobID: a.ID, JobVersionID: av.ID, LaunchMode: "api", Spec: as, TimeHour: "202610010102"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRun(ctx, CreateRunInput{JobID: b.ID, JobVersionID: bv.ID, LaunchMode: "api", Spec: bs, TimeHour: "202610010101"}); err != nil {
		t.Fatal(err)
	}
	latest, err := st.LatestRunsByJob(ctx, []uuid.UUID{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := latest[a.ID].RunID; got != r2.Run.ID {
		t.Fatalf("latest job a=%s want %s", got, r2.Run.ID)
	}
	if got := latest[a.ID].State; got != string(domain.RunQueued) {
		t.Fatalf("latest state=%s", got)
	}
	if _, ok := latest[b.ID]; !ok {
		t.Fatal("missing latest run for job b")
	}
}

func TestTimeHourRequiredAndValid(t *testing.T) {
	st, ctx := testStore(t)
	job, version, _, _ := mkJobRun(t, st, ctx)
	var nulls int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM run WHERE time_hour IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatalf("time_hour null rows=%d", nulls)
	}
	var idx *string
	if err := st.Pool().QueryRow(ctx, `SELECT to_regclass('public.run_job_time_hour_idx')::text`).Scan(&idx); err != nil || idx == nil {
		t.Fatalf("run_job_time_hour_idx missing: %v %v", idx, err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO run (job_id, job_version_id, launch_mode, state, command_snapshot, time_hour)
		VALUES ($1,$2,'api','QUEUED','{}'::jsonb,NULL)`, job.ID, version.ID); err == nil {
		t.Fatal("NULL time_hour must be rejected")
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO run (job_id, job_version_id, launch_mode, state, command_snapshot, time_hour)
		VALUES ($1,$2,'api','QUEUED','{}'::jsonb,'2026-10-24')`, job.ID, version.ID); err == nil {
		t.Fatal("malformed time_hour must be rejected")
	}
	if _, err := st.CreateRun(ctx, CreateRunInput{
		JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api", Spec: domain.JobSpec{Command: []string{"true"}}, TimeHour: "bad",
	}); err == nil {
		t.Fatal("CreateRun must validate time_hour")
	}
	created, err := st.CreateRun(ctx, CreateRunInput{
		JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api", Spec: domain.JobSpec{Command: []string{"true"}},
	})
	if err != nil || domain.ValidateTimeHour(created.Run.TimeHour) != nil {
		t.Fatalf("default time_hour invalid: %+v %v", created.Run, err)
	}
}

func TestCronCatchUpAllMissedOccurrences(t *testing.T) {
	st, ctx := testStore(t)
	job, err := st.CreateJob(ctx, CreateJobInput{Name: "cron-catchup-" + uuid.NewString(), CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	spec := domain.JobSpec{Command: []string{"echo hi"}}
	spec.Normalize()
	hash, _ := spec.Hash()
	if _, err := st.PublishJobVersion(ctx, job.ID, spec, hash, "test"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	due := now.Add(-4 * time.Minute)
	if _, err := st.UpdateSchedule(ctx, job.ID, UpdateScheduleInput{
		Cron: stringPtr("* * * * *"), Timezone: "UTC", ScheduleEnabled: true, NextFireAt: &due,
	}); err != nil {
		t.Fatal(err)
	}
	fired, next, err := st.FireDueJobCatchUp(ctx, job.ID, due, 10)
	if err != nil {
		t.Fatalf("catch up: %v", err)
	}
	if fired != 5 {
		t.Fatalf("fired=%d, want 5 missed occurrences", fired)
	}
	if !next.Equal(now.Add(time.Minute)) {
		t.Fatalf("next=%v, want %v", next, now.Add(time.Minute))
	}
	var runs int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM run WHERE job_id=$1 AND launch_mode='cron'`, job.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 5 {
		t.Fatalf("runs=%d, want 5", runs)
	}
	var scheduled int
	if err := st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM run WHERE job_id=$1 AND scheduled_at IN ($2,$3,$4,$5,$6)`,
		job.ID, due, due.Add(time.Minute), due.Add(2*time.Minute), due.Add(3*time.Minute), due.Add(4*time.Minute)).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled != 5 {
		t.Fatalf("scheduled_at rows=%d, want 5", scheduled)
	}
	fired, _, err = st.FireDueJobCatchUp(ctx, job.ID, due, 10)
	if err != nil || fired != 0 {
		t.Fatalf("stale catch-up must be a no-op: fired=%d err=%v", fired, err)
	}
}

func TestControlPlaneReadsSchedulerState(t *testing.T) {
	st, ctx := testStore(t)
	nodeID := uuid.New()
	if err := st.RegisterSchedNode(ctx, nodeID, "http://master-01:8080"); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListSchedNodes(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != nodeID || nodes[0].State != "alive" {
		t.Fatalf("unexpected nodes: %+v", nodes)
	}
	old, err := st.ListSchedNodes(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(old) != 0 {
		t.Fatalf("future cutoff must be empty: %v %+v", err, old)
	}

	guard, err := st.GetLeaderGuard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if guard.OwnerID != "" || !guard.ExpiresAt.Before(time.Now()) {
		t.Fatalf("empty guard: %+v", guard)
	}
	if ok, err := st.TryLeaderGuard(ctx, nodeID, "redis", 5*time.Second); err != nil || !ok {
		t.Fatalf("acquire guard: %v %v", ok, err)
	}
	guard, err = st.GetLeaderGuard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if guard.OwnerID != nodeID.String() || guard.Mode != "redis" || !guard.ExpiresAt.After(time.Now()) {
		t.Fatalf("active guard: %+v", guard)
	}
}

func TestLeaderGuardExclusive(t *testing.T) {
	st, ctx := testStore(t)
	a, b := uuid.New(), uuid.New()
	ok, err := st.TryLeaderGuard(ctx, a, "redis", 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("node-a acquire: %v %v", ok, err)
	}
	if ok, err := st.TryLeaderGuard(ctx, b, "pg", 5*time.Second); err != nil || ok {
		t.Fatalf("node-b must not acquire while node-a holds: %v %v", ok, err)
	}
	if ok, err := st.RenewLeaderGuard(ctx, a, 5*time.Second); err != nil || !ok {
		t.Fatalf("node-a renew: %v %v", ok, err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE leader_guard SET expires_at=clock_timestamp()-interval '1 second' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.TryLeaderGuard(ctx, b, "pg", 5*time.Second); err != nil || !ok {
		t.Fatalf("node-b acquire after expiry: %v %v", ok, err)
	}
	if ok, err := st.RenewLeaderGuard(ctx, a, 5*time.Second); err != nil || ok {
		t.Fatalf("node-a must not renew after takeover: %v %v", ok, err)
	}
	if err := st.ReleaseLeaderGuard(ctx, b); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.TryLeaderGuard(ctx, a, "redis", 5*time.Second); err != nil || !ok {
		t.Fatalf("node-a acquire after release: %v %v", ok, err)
	}
}

func TestLeaderSessionExclusive(t *testing.T) {
	st, ctx := testStore(t)
	first, ok, err := st.AcquireLeaderSession(ctx)
	if err != nil || !ok {
		t.Fatalf("first session: %v %v", ok, err)
	}
	second, ok, err := st.AcquireLeaderSession(ctx)
	if err != nil || ok {
		if second != nil {
			second.Release(ctx)
		}
		t.Fatalf("second session must not acquire: %v %v", ok, err)
	}
	if !first.Alive(ctx) {
		t.Fatal("first session should be alive")
	}
	first.Release(ctx)
	third, ok, err := st.AcquireLeaderSession(ctx)
	if err != nil || !ok {
		t.Fatalf("session after release: %v %v", ok, err)
	}
	third.Release(ctx)
}

func TestClaimComplete(t *testing.T) {
	st, ctx := testStore(t)
	_, _, run, workerID := mkJobRun(t, st, ctx)

	claimed, err := st.ClaimRuns(ctx, workerID, []string{"linux"}, 2)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].RunID != run.ID {
		t.Fatalf("unexpected claim: %+v", claimed)
	}
	zero := 0
	exitOne := 1
	result, err := st.CompleteRun(ctx, CompleteRunInput{
		RunID: run.ID, WorkerID: workerID, LeaseToken: claimed[0].LeaseToken, ExitCode: &zero,
	})
	if err != nil || result.State != domain.RunSucceeded {
		t.Fatalf("complete: %+v %v", result, err)
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil || got.State != domain.RunSucceeded || got.FinishedAt == nil {
		t.Fatalf("stored run: %+v %v", got, err)
	}
	again, err := st.CompleteRun(ctx, CompleteRunInput{
		RunID: run.ID, WorkerID: workerID, LeaseToken: claimed[0].LeaseToken, ExitCode: &zero,
	})
	if err != nil || !again.Ignored || again.State != domain.RunSucceeded || again.Attempt != 1 {
		t.Fatalf("duplicate terminal completion: %+v %v", again, err)
	}
	bogus := uuid.New()
	ignored, err := st.CompleteRun(ctx, CompleteRunInput{
		RunID: run.ID, WorkerID: workerID, LeaseToken: bogus, ExitCode: &exitOne,
	})
	if err != nil || !ignored.Ignored || ignored.State != domain.RunSucceeded {
		t.Fatalf("terminal completion must ignore token: %+v %v", ignored, err)
	}
}

func TestCompleteRunningTokenMismatch(t *testing.T) {
	st, ctx := testStore(t)
	_, _, run, workerID := mkJobRun(t, st, ctx)
	claimed, err := st.ClaimRuns(ctx, workerID, []string{"linux"}, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	zero := 0
	if _, err := st.CompleteRun(ctx, CompleteRunInput{
		RunID: run.ID, WorkerID: workerID, LeaseToken: uuid.New(), ExitCode: &zero,
	}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("running token mismatch should be lease lost, got %v", err)
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil || got.State != domain.RunRunning {
		t.Fatalf("run must stay running: %+v %v", got, err)
	}
}

func TestIdempotencyReturnsSameRun(t *testing.T) {
	st, ctx := testStore(t)
	job, version, _, _ := mkJobRun(t, st, ctx)
	spec := domain.JobSpec{Command: []string{"true"}}
	spec.Normalize()
	key := "idem-" + uuid.NewString()
	first, err := st.CreateRun(ctx, CreateRunInput{JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api", Spec: spec, IdempotencyKey: &key})
	if err != nil || !first.Created {
		t.Fatalf("first create: %+v %v", first, err)
	}
	second, err := st.CreateRun(ctx, CreateRunInput{JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api", Spec: spec, IdempotencyKey: &key})
	if err != nil || second.Created || second.Run.ID != first.Run.ID {
		t.Fatalf("duplicate idempotency: %+v %v", second, err)
	}
}

func TestFailureIsTerminal(t *testing.T) {
	st, ctx := testStore(t)
	_, _, run, workerID := mkJobRun(t, st, ctx)
	claimed, err := st.ClaimRuns(ctx, workerID, []string{"linux"}, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	exit := 1
	result, err := st.CompleteRun(ctx, CompleteRunInput{RunID: run.ID, WorkerID: workerID, LeaseToken: claimed[0].LeaseToken, ExitCode: &exit, Error: "exit 1"})
	if err != nil || result.State != domain.RunFailed || result.Attempt != 1 {
		t.Fatalf("failure should be terminal: %+v %v", result, err)
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil || got.State != domain.RunFailed || got.FinishedAt == nil {
		t.Fatalf("failed run: %+v %v", got, err)
	}
	again, err := st.ClaimRuns(ctx, workerID, []string{"linux"}, 1)
	if err != nil || len(again) != 0 {
		t.Fatalf("failed run must not be claimable: %+v %v", again, err)
	}
}

func TestCancelQueuedRun(t *testing.T) {
	st, ctx := testStore(t)
	job, version, _, _ := mkJobRun(t, st, ctx)
	spec := domain.JobSpec{Command: []string{"true"}}
	spec.Normalize()
	created, err := st.CreateRun(ctx, CreateRunInput{JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api", Spec: spec})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.CancelRun(ctx, created.Run.ID)
	if err != nil || got.State != domain.RunCanceled {
		t.Fatalf("cancel: %+v %v", got, err)
	}
}

func mkJobVersion(t *testing.T, st *Store, ctx context.Context, name string) (domain.Job, domain.JobVersion, domain.JobSpec) {
	t.Helper()
	job, err := st.CreateJob(ctx, CreateJobInput{Name: name, CreatedBy: "test"})
	if err != nil {
		t.Fatalf("create job %s: %v", name, err)
	}
	spec := domain.JobSpec{Command: []string{"true"}, Labels: []string{"linux"}}
	spec.Normalize()
	hash, _ := spec.Hash()
	version, err := st.PublishJobVersion(ctx, job.ID, spec, hash, "test")
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	return job, version, spec
}

func mkCycleRun(t *testing.T, st *Store, ctx context.Context, job domain.Job, version domain.JobVersion, spec domain.JobSpec, worker uuid.UUID, cycle string) domain.Run {
	t.Helper()
	res, err := st.CreateRun(ctx, CreateRunInput{
		JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api",
		Spec: spec, Priority: 1, TimeHour: timeHourFromCycle(cycle), CycleKey: &cycle,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return res.Run
}

func completeRunOK(t *testing.T, st *Store, ctx context.Context, runID, worker uuid.UUID) {
	t.Helper()
	claimed, err := st.ClaimRuns(ctx, worker, []string{"linux"}, 5)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	var token uuid.UUID
	for _, c := range claimed {
		if c.RunID == runID {
			token = c.LeaseToken
		}
	}
	if token == uuid.Nil {
		t.Fatalf("run %s not claimed: %+v", runID, claimed)
	}
	zero := 0
	if _, err := st.CompleteRun(ctx, CompleteRunInput{RunID: runID, WorkerID: worker, LeaseToken: token, ExitCode: &zero}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func stringPtr(v string) *string { return &v }
func intPtr(v int) *int          { return &v }

func timeHourFromCycle(cycle string) string {
	if t, err := time.Parse("2006-01-02", cycle); err == nil {
		return t.Format("20060102") + "0000"
	}
	return "202610240000"
}

func depRunCount(t *testing.T, st *Store, ctx context.Context, jobID uuid.UUID, timeHour string) int {
	t.Helper()
	var count int
	if err := st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM run
		WHERE job_id=$1 AND time_hour=$2 AND launch_mode='dependency'`, jobID, timeHour).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestClaimRunsReturnsTriggerProvenance(t *testing.T) {
	st, ctx := testStore(t)
	upstream, upstreamVersion, upstreamSpec := mkJobVersion(t, st, ctx, "provenance-upstream")
	downstream, _, _ := mkJobVersion(t, st, ctx, "provenance-downstream")
	worker := uuid.New()
	if _, err := st.RegisterWorker(ctx, RegisterWorkerInput{ID: worker, Labels: []string{"linux"}, ConcurrencyLimit: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetJobDependencies(ctx, downstream.ID, []DependencyEdge{
		{UpstreamJobID: upstream.ID, Condition: "on_success", Required: true},
	}); err != nil {
		t.Fatal(err)
	}
	cycle := "2026-10-24"
	upstreamRun := mkCycleRun(t, st, ctx, upstream, upstreamVersion, upstreamSpec, worker, cycle)
	completeRunOK(t, st, ctx, upstreamRun.ID, worker)

	var downstreamRunID uuid.UUID
	if err := st.Pool().QueryRow(ctx, `
		SELECT id FROM run
		WHERE job_id=$1 AND launch_mode='dependency' AND time_hour=$2`,
		downstream.ID, timeHourFromCycle(cycle)).Scan(&downstreamRunID); err != nil {
		t.Fatalf("find dependency run: %v", err)
	}
	claimed, err := st.ClaimRuns(ctx, worker, []string{"linux"}, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim downstream: %+v %v", claimed, err)
	}
	got := claimed[0]
	if got.RunID != downstreamRunID {
		t.Fatalf("claimed run=%s, want %s", got.RunID, downstreamRunID)
	}
	if got.JobName != downstream.Name {
		t.Fatalf("job name=%q, want %q", got.JobName, downstream.Name)
	}
	if got.LaunchMode != "dependency" {
		t.Fatalf("launch mode=%q, want dependency", got.LaunchMode)
	}
	if got.TriggeredByRunID == nil || *got.TriggeredByRunID != upstreamRun.ID {
		t.Fatalf("triggered by run=%v, want %s", got.TriggeredByRunID, upstreamRun.ID)
	}
	if got.TriggeredByJobName == nil || *got.TriggeredByJobName != upstream.Name {
		t.Fatalf("triggered by job=%v, want %q", got.TriggeredByJobName, upstream.Name)
	}
}

func TestDependencyFanInTriggersAndReruns(t *testing.T) {
	st, ctx := testStore(t)
	a, av, as := mkJobVersion(t, st, ctx, "dep-a")
	b, bv, bs := mkJobVersion(t, st, ctx, "dep-b")
	d, _, _ := mkJobVersion(t, st, ctx, "dep-d")
	worker := uuid.New()
	if _, err := st.RegisterWorker(ctx, RegisterWorkerInput{ID: worker, Labels: []string{"linux"}, ConcurrencyLimit: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetJobDependencies(ctx, d.ID, []DependencyEdge{
		{UpstreamJobID: a.ID, Condition: "on_success", Required: true},
		{UpstreamJobID: b.ID, Condition: "on_success", Required: true},
	}); err != nil {
		t.Fatal(err)
	}
	cycle := "2026-10-24"
	timeHour := timeHourFromCycle(cycle)

	runA := mkCycleRun(t, st, ctx, a, av, as, worker, cycle)
	completeRunOK(t, st, ctx, runA.ID, worker)
	if got := depRunCount(t, st, ctx, d.ID, timeHour); got != 0 {
		t.Fatalf("downstream runs after one upstream=%d", got)
	}

	runB := mkCycleRun(t, st, ctx, b, bv, bs, worker, cycle)
	completeRunOK(t, st, ctx, runB.ID, worker)
	if got := depRunCount(t, st, ctx, d.ID, timeHour); got != 1 {
		t.Fatalf("downstream runs after fan-in=%d, want 1", got)
	}

	// A backfill/rerun of the same time_hour may trigger downstream again.
	runA2 := mkCycleRun(t, st, ctx, a, av, as, worker, cycle)
	completeRunOK(t, st, ctx, runA2.ID, worker)
	if got := depRunCount(t, st, ctx, d.ID, timeHour); got != 2 {
		t.Fatalf("downstream runs after rerun=%d, want 2", got)
	}
}

func TestDependencyRequiredFailureSkips(t *testing.T) {
	st, ctx := testStore(t)
	a, av, as := mkJobVersion(t, st, ctx, "fail-a")
	d, _, _ := mkJobVersion(t, st, ctx, "fail-d")
	worker := uuid.New()
	if _, err := st.RegisterWorker(ctx, RegisterWorkerInput{ID: worker, Labels: []string{"linux"}, ConcurrencyLimit: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetJobDependencies(ctx, d.ID, []DependencyEdge{
		{UpstreamJobID: a.ID, Condition: "on_success", Required: true},
	}); err != nil {
		t.Fatal(err)
	}
	cycle := "2026-10-24"
	runA := mkCycleRun(t, st, ctx, a, av, as, worker, cycle)
	claimed, err := st.ClaimRuns(ctx, worker, []string{"linux"}, 2)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	exit := 1
	if _, err := st.CompleteRun(ctx, CompleteRunInput{RunID: runA.ID, WorkerID: worker, LeaseToken: claimed[0].LeaseToken, ExitCode: &exit, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if got := depRunCount(t, st, ctx, d.ID, timeHourFromCycle(cycle)); got != 0 {
		t.Fatalf("downstream runs after required failure=%d", got)
	}
}

func TestDependencyTriggersAfterUpstreamRecoveryAndManualDownstream(t *testing.T) {
	st, ctx := testStore(t)
	a, av, as := mkJobVersion(t, st, ctx, "recover-a")
	d, dv, ds := mkJobVersion(t, st, ctx, "recover-d")
	worker := uuid.New()
	if _, err := st.RegisterWorker(ctx, RegisterWorkerInput{ID: worker, Labels: []string{"linux"}, ConcurrencyLimit: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetJobDependencies(ctx, d.ID, []DependencyEdge{
		{UpstreamJobID: a.ID, Condition: "on_success", Required: true},
	}); err != nil {
		t.Fatal(err)
	}
	cycle := "2026-10-24"
	timeHour := timeHourFromCycle(cycle)

	// Manual downstream trigger never waits for dependency readiness. Drain the
	// manual run before claiming the upstream failure so the lease is unambiguous.
	manual, err := st.CreateRun(ctx, CreateRunInput{
		JobID: d.ID, JobVersionID: dv.ID, LaunchMode: "api", Spec: ds, TimeHour: timeHour,
	})
	if err != nil {
		t.Fatalf("manual downstream run: %v", err)
	}
	claimed, err := st.ClaimRuns(ctx, worker, []string{"linux"}, 2)
	if err != nil || len(claimed) != 1 || claimed[0].RunID != manual.Run.ID {
		t.Fatalf("claim manual run: %+v %v", claimed, err)
	}
	if _, err := st.CompleteRun(ctx, CompleteRunInput{RunID: manual.Run.ID, WorkerID: worker, LeaseToken: claimed[0].LeaseToken, ExitCode: intPtr(0)}); err != nil {
		t.Fatal(err)
	}

	runA := mkCycleRun(t, st, ctx, a, av, as, worker, cycle)
	claimed, err = st.ClaimRuns(ctx, worker, []string{"linux"}, 2)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim failed run: %+v %v", claimed, err)
	}
	exit := 1
	if _, err := st.CompleteRun(ctx, CompleteRunInput{RunID: runA.ID, WorkerID: worker, LeaseToken: claimed[0].LeaseToken, ExitCode: &exit, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if got := depRunCount(t, st, ctx, d.ID, timeHour); got != 0 {
		t.Fatalf("downstream after required failure=%d", got)
	}

	// A successful rerun of the same time_hour must trigger the downstream now.
	runA2 := mkCycleRun(t, st, ctx, a, av, as, worker, cycle)
	claimed, err = st.ClaimRuns(ctx, worker, []string{"linux"}, 2)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim rerun: %+v %v", claimed, err)
	}
	if _, err := st.CompleteRun(ctx, CompleteRunInput{RunID: runA2.ID, WorkerID: worker, LeaseToken: claimed[0].LeaseToken, ExitCode: intPtr(0)}); err != nil {
		t.Fatal(err)
	}
	if got := depRunCount(t, st, ctx, d.ID, timeHour); got != 1 {
		t.Fatalf("downstream after upstream recovery=%d, want 1", got)
	}
}

func TestDependencyCycleRejected(t *testing.T) {
	st, ctx := testStore(t)
	a, _, _ := mkJobVersion(t, st, ctx, "cycle-a")
	b, _, _ := mkJobVersion(t, st, ctx, "cycle-b")
	if _, err := st.SetJobDependencies(ctx, a.ID, []DependencyEdge{{UpstreamJobID: b.ID, Required: true}}); err != nil {
		t.Fatal(err)
	}
	_, err := st.SetJobDependencies(ctx, b.ID, []DependencyEdge{{UpstreamJobID: a.ID, Required: true}})
	if err == nil {
		t.Fatal("expected dependency cycle to be rejected")
	}
}

func TestDependencyNonRequiredFailureIgnored(t *testing.T) {
	st, ctx := testStore(t)
	a, av, as := mkJobVersion(t, st, ctx, "optional-a")
	b, bv, bs := mkJobVersion(t, st, ctx, "required-b")
	d, _, _ := mkJobVersion(t, st, ctx, "optional-d")
	worker := uuid.New()
	if _, err := st.RegisterWorker(ctx, RegisterWorkerInput{ID: worker, Labels: []string{"linux"}, ConcurrencyLimit: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetJobDependencies(ctx, d.ID, []DependencyEdge{
		{UpstreamJobID: a.ID, Required: false},
		{UpstreamJobID: b.ID, Required: true},
	}); err != nil {
		t.Fatal(err)
	}
	cycle := "2026-10-26"
	runA := mkCycleRun(t, st, ctx, a, av, as, worker, cycle)
	claimed, err := st.ClaimRuns(ctx, worker, []string{"linux"}, 4)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	exit := 1
	if _, err := st.CompleteRun(ctx, CompleteRunInput{RunID: runA.ID, WorkerID: worker, LeaseToken: claimed[0].LeaseToken, ExitCode: &exit, Error: "optional failure"}); err != nil {
		t.Fatal(err)
	}

	runB := mkCycleRun(t, st, ctx, b, bv, bs, worker, cycle)
	completeRunOK(t, st, ctx, runB.ID, worker)
	if got := depRunCount(t, st, ctx, d.ID, timeHourFromCycle(cycle)); got != 1 {
		t.Fatalf("downstream runs after required success=%d, want 1", got)
	}
}

func TestDependencyTimeHourIsolation(t *testing.T) {
	st, ctx := testStore(t)
	a, av, as := mkJobVersion(t, st, ctx, "isolation-a")
	d, _, _ := mkJobVersion(t, st, ctx, "isolation-d")
	worker := uuid.New()
	if _, err := st.RegisterWorker(ctx, RegisterWorkerInput{ID: worker, Labels: []string{"linux"}, ConcurrencyLimit: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetJobDependencies(ctx, d.ID, []DependencyEdge{{UpstreamJobID: a.ID, Required: true}}); err != nil {
		t.Fatal(err)
	}
	for _, cycle := range []string{"2026-10-24", "2026-10-25"} {
		runA := mkCycleRun(t, st, ctx, a, av, as, worker, cycle)
		completeRunOK(t, st, ctx, runA.ID, worker)
		if got := depRunCount(t, st, ctx, d.ID, timeHourFromCycle(cycle)); got != 1 {
			t.Fatalf("cycle %s dependency runs=%d", cycle, got)
		}
	}
}

func TestWorkerCRUDAndHistory(t *testing.T) {
	st, ctx := testStore(t)
	worker, err := st.CreateWorker(ctx, WorkerInput{
		Name: "linux-runner-1", Description: "primary executor",
		Labels: []string{"linux", "gpu"}, ConcurrencyLimit: 4,
	})
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	if worker.Name != "linux-runner-1" || worker.Description != "primary executor" ||
		worker.ConcurrencyLimit != 4 || worker.Status != "OFFLINE" {
		t.Fatalf("created worker=%+v", worker)
	}
	if time.Since(worker.LastHeartbeatAt) < time.Hour {
		t.Fatalf("created worker should not look recently alive: %s", worker.LastHeartbeatAt)
	}
	if _, err := st.CreateWorker(ctx, WorkerInput{Name: "LINUX-RUNNER-1", ConcurrencyLimit: 2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name should conflict, got %v", err)
	}

	updated, err := st.UpdateWorker(ctx, worker.ID, WorkerInput{
		Name: "linux-runner-primary", Description: "edited", Labels: []string{"linux"}, ConcurrencyLimit: 8,
	})
	if err != nil {
		t.Fatalf("update worker: %v", err)
	}
	if updated.Name != "linux-runner-primary" || updated.ConcurrencyLimit != 8 || updated.Description != "edited" {
		t.Fatalf("updated worker=%+v", updated)
	}
	registered, err := st.RegisterWorker(ctx, RegisterWorkerInput{
		ID: worker.ID, Labels: []string{"linux"}, ConcurrencyLimit: 8,
		Hostname: "runner-host", Version: "test",
	})
	if err != nil {
		t.Fatalf("register existing worker: %v", err)
	}
	if registered.Name != "linux-runner-primary" || registered.Description != "edited" ||
		registered.Status != "ONLINE" || registered.Hostname != "runner-host" {
		t.Fatalf("registration overwrote managed identity: %+v", registered)
	}

	job, version, spec := mkJobVersion(t, st, ctx, "worker-history")
	run, err := st.CreateRun(ctx, CreateRunInput{
		JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api",
		Spec: spec, TimeHour: "202610301200",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, err := st.ClaimRuns(ctx, worker.ID, []string{"linux"}, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	zero := 0
	if _, err := st.CompleteRun(ctx, CompleteRunInput{
		RunID: run.Run.ID, WorkerID: worker.ID, LeaseToken: claimed[0].LeaseToken, ExitCode: &zero,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	history, err := st.ListWorkerRuns(ctx, worker.ID, 20)
	if err != nil || len(history) != 1 || history[0].RunID != run.Run.ID || history[0].State != string(domain.RunSucceeded) {
		t.Fatalf("worker history=%+v err=%v", history, err)
	}

	// An executor with a RUNNING build cannot be deleted.
	busy, err := st.CreateRun(ctx, CreateRunInput{
		JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api",
		Spec: spec, TimeHour: "202610301201",
	})
	if err != nil {
		t.Fatalf("create busy run: %v", err)
	}
	busyClaim, err := st.ClaimRuns(ctx, worker.ID, []string{"linux"}, 1)
	if err != nil || len(busyClaim) != 1 {
		t.Fatalf("claim busy: %+v %v", busyClaim, err)
	}
	if err := st.DeleteWorker(ctx, worker.ID); !errors.Is(err, ErrWorkerBusy) {
		t.Fatalf("delete busy executor should conflict, got %v", err)
	}
	if _, err := st.CompleteRun(ctx, CompleteRunInput{
		RunID: busy.Run.ID, WorkerID: worker.ID, LeaseToken: busyClaim[0].LeaseToken, ExitCode: &zero,
	}); err != nil {
		t.Fatalf("complete busy run: %v", err)
	}
	if err := st.DeleteWorker(ctx, worker.ID); err != nil {
		t.Fatalf("delete idle executor: %v", err)
	}
	if _, err := st.GetWorker(ctx, worker.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted executor should be gone, got %v", err)
	}
}

func TestWorkerDrainPersistsAcrossRegister(t *testing.T) {
	st, ctx := testStore(t)
	id := uuid.New()
	if _, err := st.RegisterWorker(ctx, RegisterWorkerInput{ID: id, Labels: []string{"linux"}, ConcurrencyLimit: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkerDraining(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	w, err := st.RegisterWorker(ctx, RegisterWorkerInput{ID: id, Labels: []string{"linux"}, ConcurrencyLimit: 2})
	if err != nil || !w.Draining || w.Status != "DRAINING" {
		t.Fatalf("re-register worker=%+v err=%v", w, err)
	}
	if err := st.SetWorkerDraining(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	w, err = st.GetWorker(ctx, id)
	if err != nil || w.Draining || w.Status != "ONLINE" {
		t.Fatalf("resume worker=%+v err=%v", w, err)
	}
}

func TestListRunningRunsByWorker(t *testing.T) {
	st, ctx := testStore(t)
	job, version, run, workerID := mkJobRun(t, st, ctx)
	claimed, err := st.ClaimRuns(ctx, workerID, []string{"linux"}, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	// A queued run must not appear in the executor's live-build list.
	queuedSpec := domain.JobSpec{Command: []string{"true"}, Labels: []string{"linux"}}
	queuedSpec.Normalize()
	queued, err := st.CreateRun(ctx, CreateRunInput{
		JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api",
		Spec: queuedSpec, TimeHour: "202610291200",
	})
	if err != nil {
		t.Fatalf("create queued run: %v", err)
	}

	live, err := st.ListRunningRuns(ctx, 100)
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("running runs=%+v", live)
	}
	got := live[0]
	if got.WorkerID != workerID || got.RunID != run.ID || got.JobID != job.ID ||
		got.JobName != job.Name || got.LaunchMode != "api" || got.Attempt != 1 ||
		got.StartedAt.IsZero() || domain.ValidateTimeHour(got.TimeHour) != nil {
		t.Fatalf("unexpected running run: %+v", got)
	}
	if got.RunID == queued.Run.ID {
		t.Fatal("queued run leaked into running result")
	}

	zero := 0
	if _, err := st.CompleteRun(ctx, CompleteRunInput{
		RunID: run.ID, WorkerID: workerID, LeaseToken: claimed[0].LeaseToken, ExitCode: &zero,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	live, err = st.ListRunningRuns(ctx, 100)
	if err != nil || len(live) != 0 {
		t.Fatalf("after completion running=%+v err=%v", live, err)
	}
}

func TestFailWorkerRunsFencesStaleCompletion(t *testing.T) {
	st, ctx := testStore(t)
	_, _, run, workerID := mkJobRun(t, st, ctx)
	claimed, err := st.ClaimRuns(ctx, workerID, []string{"linux"}, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil || got.LeaseToken == nil || got.LeaseOwner == nil {
		t.Fatalf("run should have owner token fencing: %+v %v", got, err)
	}
	owners, err := st.RunningOwners(ctx, 10, 5*time.Second)
	if err != nil || len(owners) != 1 || owners[0].WorkerID != workerID || !owners[0].DBFresh {
		t.Fatalf("running owners: %+v %v", owners, err)
	}
	n, err := st.FailWorkerRuns(ctx, workerID, 10)
	if err != nil || n != 1 {
		t.Fatalf("fail worker runs: %d %v", n, err)
	}
	got, err = st.GetRun(ctx, run.ID)
	if err != nil || got.State != domain.RunFailed || got.LeaseOwner != nil || got.LeaseToken != nil || got.Error == nil || *got.Error == "" || got.FinishedAt == nil {
		t.Fatalf("failed run: %+v %v", got, err)
	}
	zero := 0
	late, err := st.CompleteRun(ctx, CompleteRunInput{
		RunID: run.ID, WorkerID: workerID, LeaseToken: claimed[0].LeaseToken, ExitCode: &zero,
	})
	if err != nil || !late.Ignored || late.State != domain.RunFailed {
		t.Fatalf("late completion should be an ignored no-op: %+v %v", late, err)
	}
}

func TestCancelRunningEnqueuesCommand(t *testing.T) {
	st, ctx := testStore(t)
	_, _, run, workerID := mkJobRun(t, st, ctx)
	if _, err := st.ClaimRuns(ctx, workerID, []string{"linux"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CancelRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var kind, payload string
	if err := st.Pool().QueryRow(ctx, `
		SELECT kind, payload::text FROM worker_command
		WHERE worker_id=$1 AND run_id=$2`, workerID, run.ID).Scan(&kind, &payload); err != nil {
		t.Fatalf("cancel command: %v", err)
	}
	if kind != "cancel" || !strings.Contains(payload, run.ID.String()) {
		t.Fatalf("unexpected cancel command kind=%s payload=%s", kind, payload)
	}
}

func TestCompleteAfterCancelIsCanceled(t *testing.T) {
	st, ctx := testStore(t)
	_, _, run, workerID := mkJobRun(t, st, ctx)
	claimed, err := st.ClaimRuns(ctx, workerID, []string{"linux"}, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if _, err := st.CancelRun(ctx, run.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	killed := -1
	result, err := st.CompleteRun(ctx, CompleteRunInput{
		RunID: run.ID, WorkerID: workerID, LeaseToken: claimed[0].LeaseToken,
		ExitCode: &killed, Error: "signal: killed",
	})
	if err != nil || result.State != domain.RunCanceled || result.Ignored {
		t.Fatalf("cancel completion: %+v %v", result, err)
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil || got.State != domain.RunCanceled || got.Error == nil || *got.Error != "canceled" {
		t.Fatalf("stored canceled run: %+v %v", got, err)
	}
}

func TestArchiveRetentionKeepsNewestOrRecentRuns(t *testing.T) {
	st, ctx := testStore(t)
	job, version, spec := mkJobVersion(t, st, ctx, "retention-job")
	if _, err := st.UpdateLogRetention(ctx, job.ID, 2, 1); err != nil {
		t.Fatalf("update retention: %v", err)
	}
	create := func() domain.Run {
		res, err := st.CreateRun(ctx, CreateRunInput{
			JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api",
			Spec: spec, TimeHour: "202610240000",
		})
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		return res.Run
	}
	runs := []domain.Run{create(), create(), create(), create()}
	ages := []time.Duration{5 * 24 * time.Hour, 4 * 24 * time.Hour, 3 * 24 * time.Hour, 30 * time.Minute}
	for i, run := range runs {
		at := time.Now().Add(-ages[i])
		if _, err := st.Pool().Exec(ctx, `
			UPDATE run SET state='SUCCEEDED', created_at=$2, finished_at=$2, archived_at=NULL
			WHERE id=$1`, run.ID, at); err != nil {
			t.Fatalf("age run %d: %v", i, err)
		}
	}

	candidates, locked, err := st.ClaimRunsForArchive(ctx, 100)
	if err != nil {
		t.Fatalf("claim archive: %v", err)
	}
	if !locked || len(candidates) != 2 {
		t.Fatalf("candidates=%+v locked=%v, want 2", candidates, locked)
	}
	if candidates[0].RunID != runs[0].ID || candidates[1].RunID != runs[1].ID {
		t.Fatalf("archived wrong runs: %+v", candidates)
	}
	again, _, err := st.ClaimRunsForArchive(ctx, 100)
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim=%+v err=%v", again, err)
	}

	runsPage, err := st.ListRuns(ctx, RunFilter{JobID: &job.ID, Limit: 10})
	if err != nil || len(runsPage.Runs) != 2 {
		t.Fatalf("visible runs=%d err=%v, want 2", len(runsPage.Runs), err)
	}
	latest, err := st.LatestRunsByJob(ctx, []uuid.UUID{job.ID})
	if err != nil {
		t.Fatalf("latest runs: %v", err)
	}
	if last, ok := latest[job.ID]; !ok || last.RunID != runs[3].ID {
		t.Fatalf("latest=%+v, want newest run", last)
	}
	archived, err := st.GetRun(ctx, runs[0].ID)
	if err != nil || archived.ArchivedAt == nil {
		t.Fatalf("archived run=%+v err=%v", archived, err)
	}
}

func TestDependencyReadinessIgnoresArchivedUpstreamSuccess(t *testing.T) {
	st, ctx := testStore(t)
	upstream, version, spec := mkJobVersion(t, st, ctx, "archive-upstream")
	downstream, _, _ := mkJobVersion(t, st, ctx, "archive-downstream")
	if _, err := st.SetJobDependencies(ctx, downstream.ID, []DependencyEdge{
		{UpstreamJobID: upstream.ID, Condition: "on_success", Required: true},
	}); err != nil {
		t.Fatalf("set dependency: %v", err)
	}
	timeHour := "202610240000"
	mk := func(archived bool) domain.Run {
		res, err := st.CreateRun(ctx, CreateRunInput{
			JobID: upstream.ID, JobVersionID: version.ID, LaunchMode: "api",
			Spec: spec, TimeHour: timeHour,
		})
		if err != nil {
			t.Fatalf("create upstream run: %v", err)
		}
		var archivedAt any
		if archived {
			archivedAt = time.Now()
		}
		if _, err := st.Pool().Exec(ctx, `
			UPDATE run SET state='SUCCEEDED', finished_at=clock_timestamp(), archived_at=$2
			WHERE id=$1`, res.Run.ID, archivedAt); err != nil {
			t.Fatalf("complete upstream: %v", err)
		}
		return res.Run
	}
	advance := func(runID uuid.UUID) {
		tx, err := st.Pool().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var run domain.Run
		if err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM run WHERE id=$1`, runID), &run); err != nil {
			t.Fatalf("load run: %v", err)
		}
		if err := advanceDependencies(ctx, tx, &run); err != nil {
			t.Fatalf("advance dependencies: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}

	archivedRun := mk(true)
	advance(archivedRun.ID)
	if got := depRunCount(t, st, ctx, downstream.ID, timeHour); got != 0 {
		t.Fatalf("archived upstream triggered %d downstream runs", got)
	}

	liveRun := mk(false)
	advance(liveRun.ID)
	if got := depRunCount(t, st, ctx, downstream.ID, timeHour); got != 1 {
		t.Fatalf("live upstream triggered %d downstream runs, want 1", got)
	}
}

func TestArchiveJobSoftArchiveAndRename(t *testing.T) {
	st, ctx := testStore(t)
	upstream, err := st.CreateJob(ctx, CreateJobInput{Name: "archive-up-" + uuid.NewString(), CreatedBy: "test"})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	job, _, run, _ := mkJobRun(t, st, ctx)
	if _, err := st.SetJobDependencies(ctx, job.ID, []DependencyEdge{{UpstreamJobID: upstream.ID, Condition: "on_success", Required: true}}); err != nil {
		t.Fatalf("set dependency: %v", err)
	}
	downstream, err := st.CreateJob(ctx, CreateJobInput{Name: "archive-down-" + uuid.NewString(), CreatedBy: "test"})
	if err != nil {
		t.Fatalf("create downstream: %v", err)
	}
	if _, err := st.SetJobDependencies(ctx, downstream.ID, []DependencyEdge{{UpstreamJobID: job.ID, Condition: "on_success", Required: true}}); err != nil {
		t.Fatalf("set downstream dependency: %v", err)
	}

	archived, canceled, err := st.ArchiveJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.ArchivedAt == nil || canceled != 1 {
		t.Fatalf("archived=%v canceled=%d", archived.ArchivedAt, canceled)
	}
	if archived.ScheduleEnabled {
		t.Fatal("archive must disable schedule")
	}
	gotRun, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if gotRun.State != domain.RunCanceled {
		t.Fatalf("queued run state=%s", gotRun.State)
	}
	jobs, err := st.ListJobs(ctx, JobFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	for _, j := range jobs {
		if j.ID == job.ID {
			t.Fatal("archived job must not be listed")
		}
	}
	if _, _, err := st.ArchiveJob(ctx, job.ID); err != nil {
		t.Fatalf("idempotent archive: %v", err)
	}
	jobUpstreams, jobDownstreams, err := st.ListJobDependencies(ctx, job.ID)
	if err != nil {
		t.Fatalf("list archived dependencies: %v", err)
	}
	if len(jobUpstreams) != 0 || len(jobDownstreams) != 0 {
		t.Fatalf("archive must remove both dependency sides: up=%d down=%d", len(jobUpstreams), len(jobDownstreams))
	}
	downUpstreams, _, err := st.ListJobDependencies(ctx, downstream.ID)
	if err != nil || len(downUpstreams) != 0 {
		t.Fatalf("downstream edge remains: %v %#v", err, downUpstreams)
	}

	other, err := st.CreateJob(ctx, CreateJobInput{Name: "archive-other-" + uuid.NewString(), CreatedBy: "test"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if _, err := st.RenameJob(ctx, other.ID, job.Name); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename conflict err=%v", err)
	}
	if _, err := st.RenameJob(ctx, other.ID, "archive-renamed-"+uuid.NewString()); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := st.RenameJob(ctx, job.ID, "must-not-rename"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename archived err=%v", err)
	}
}

func TestListRunsCursorPaginationStable(t *testing.T) {
	st, ctx := testStore(t)
	job, version, _, _ := mkJobRun(t, st, ctx)
	spec := domain.JobSpec{Command: []string{"true"}}
	var created []domain.Run
	for i := 0; i < 5; i++ {
		result, err := st.CreateRun(ctx, CreateRunInput{
			JobID: job.ID, JobVersionID: version.ID, LaunchMode: "api",
			Spec: spec, Priority: i,
		})
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		created = append(created, result.Run)
	}
	first, err := st.ListRuns(ctx, RunFilter{JobID: &job.ID, Limit: 2})
	if err != nil || len(first.Runs) != 2 || first.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	cursor, err := DecodeRunCursor(first.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	second, err := st.ListRuns(ctx, RunFilter{JobID: &job.ID, Limit: 2, Cursor: &cursor})
	if err != nil || len(second.Runs) != 2 || second.NextCursor == "" {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	cursor2, err := DecodeRunCursor(second.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor 2: %v", err)
	}
	third, err := st.ListRuns(ctx, RunFilter{JobID: &job.ID, Limit: 2, Cursor: &cursor2})
	if err != nil || len(third.Runs) != 2 || third.NextCursor != "" {
		t.Fatalf("third page=%+v err=%v", third, err)
	}
	seen := map[uuid.UUID]bool{}
	for _, page := range [][]domain.Run{first.Runs, second.Runs, third.Runs} {
		for _, run := range page {
			if seen[run.ID] {
				t.Fatalf("duplicate run %s", run.ID)
			}
			seen[run.ID] = true
		}
	}
	// mkJobRun already created one run for the same job.
	if len(seen) != len(created)+1 {
		t.Fatalf("seen=%d created=%d", len(seen), len(created)+1)
	}
	if _, err := st.ListRuns(ctx, RunFilter{JobID: &job.ID, Limit: 2, Cursor: &RunCursor{CreatedAt: created[0].CreatedAt, ID: created[0].ID}}); err != nil {
		t.Fatalf("cursor query: %v", err)
	}
}

func TestDependencyGraphMultiLevel(t *testing.T) {
	st, ctx := testStore(t)
	a, av, as := mkJobVersion(t, st, ctx, "graph-a")
	b, bv, bs := mkJobVersion(t, st, ctx, "graph-b")
	c, _, _ := mkJobVersion(t, st, ctx, "graph-c")
	d, _, _ := mkJobVersion(t, st, ctx, "graph-d")
	e, _, _ := mkJobVersion(t, st, ctx, "graph-e")
	f, _, _ := mkJobVersion(t, st, ctx, "graph-f")
	for downstream, upstream := range map[uuid.UUID]uuid.UUID{
		b.ID: a.ID, c.ID: b.ID, d.ID: c.ID, e.ID: d.ID, f.ID: e.ID,
	} {
		if _, err := st.SetJobDependencies(ctx, downstream, []DependencyEdge{
			{UpstreamJobID: upstream, Condition: "on_success", Required: true},
		}); err != nil {
			t.Fatalf("set dependency: %v", err)
		}
	}
	cycle := "2026-10-24"
	if _, err := st.CreateRun(ctx, CreateRunInput{
		JobID: a.ID, JobVersionID: av.ID, LaunchMode: "api", Spec: as,
		TimeHour: timeHourFromCycle(cycle),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRun(ctx, CreateRunInput{
		JobID: b.ID, JobVersionID: bv.ID, LaunchMode: "api", Spec: bs,
		TimeHour: timeHourFromCycle(cycle), Inputs: map[string]any{"artifact": "v1"},
	}); err != nil {
		t.Fatal(err)
	}

	graph, err := st.GetDependencyGraph(ctx, b.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Depth != 1 || graph.Truncated {
		t.Fatalf("graph header: depth=%d truncated=%v", graph.Depth, graph.Truncated)
	}
	nodes := map[string]DependencyGraphNode{}
	for _, node := range graph.Nodes {
		nodes[node.Name] = node
	}
	if len(nodes) != 3 {
		t.Fatalf("depth 1 nodes=%v, want graph-a/b/c", nodes)
	}
	if nodes["graph-a"].Direction != "upstream" || nodes["graph-a"].Depth != 1 {
		t.Fatalf("upstream node=%+v", nodes["graph-a"])
	}
	if nodes["graph-b"].Direction != "center" || nodes["graph-b"].Depth != 0 {
		t.Fatalf("center node=%+v", nodes["graph-b"])
	}
	if nodes["graph-c"].Direction != "downstream" || nodes["graph-c"].Depth != 1 {
		t.Fatalf("downstream node=%+v", nodes["graph-c"])
	}
	if _, ok := nodes["graph-d"]; ok {
		t.Fatal("depth 1 must not include graph-d")
	}
	if len(graph.Edges) != 2 {
		t.Fatalf("depth 1 edges=%+v, want a->b, b->c", graph.Edges)
	}
	if nodes["graph-a"].LatestRun == nil || nodes["graph-a"].LatestRun.State != "QUEUED" {
		t.Fatalf("graph-a latest run=%+v", nodes["graph-a"].LatestRun)
	}
	if nodes["graph-c"].LatestRun != nil {
		t.Fatalf("graph-c should have no run: %+v", nodes["graph-c"].LatestRun)
	}
	center := nodes["graph-b"]
	if center.LatestRun == nil || string(center.LatestRun.Inputs) != `{"artifact": "v1"}` {
		t.Fatalf("center latest run=%+v", center.LatestRun)
	}

	graph, err = st.GetDependencyGraph(ctx, c.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, node := range graph.Nodes {
		names[node.Name] = true
	}
	for _, want := range []string{"graph-a", "graph-b", "graph-c", "graph-d", "graph-e"} {
		if !names[want] {
			t.Fatalf("depth 2 missing %s: %+v", want, graph.Nodes)
		}
	}
	if _, ok := names["graph-f"]; ok {
		t.Fatal("depth 2 must not include graph-f")
	}

	// From graph-a, depth 3 must stop at graph-d, while depth 5 reaches graph-f.
	graph, err = st.GetDependencyGraph(ctx, a.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	names = map[string]bool{}
	for _, node := range graph.Nodes {
		names[node.Name] = true
	}
	if !names["graph-d"] || names["graph-e"] {
		t.Fatalf("depth 3 boundary: %+v", graph.Nodes)
	}
	graph, err = st.GetDependencyGraph(ctx, a.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	names = map[string]bool{}
	for _, node := range graph.Nodes {
		names[node.Name] = true
	}
	if !names["graph-f"] {
		t.Fatalf("depth 5 must include graph-f: %+v", graph.Nodes)
	}
}

func TestDependencyGraphTruncatesAtNodeCap(t *testing.T) {
	st, ctx := testStore(t)
	center := uuid.New()
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO job (id, name) VALUES ($1, 'cap.center')`, center); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `
		WITH new_jobs AS (
			INSERT INTO job (id, name)
			SELECT gen_random_uuid(), 'cap.child.' || g
			FROM generate_series(1, 250) AS g
			RETURNING id
		)
		INSERT INTO job_dependency (downstream_job_id, upstream_job_id)
		SELECT id, $1 FROM new_jobs`, center); err != nil {
		t.Fatal(err)
	}
	graph, err := st.GetDependencyGraph(ctx, center, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !graph.Truncated {
		t.Fatalf("graph should be truncated: nodes=%d", len(graph.Nodes))
	}
	if len(graph.Nodes) != MaxDependencyGraphNodes {
		t.Fatalf("nodes=%d, want cap %d", len(graph.Nodes), MaxDependencyGraphNodes)
	}
}
