package worker

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFormatBuildLogHeaderManual(t *testing.T) {
	workerID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	runID := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	got := formatBuildLogHeader(workerID, "worker-a", claimedRun{
		RunID: runID, JobName: "nightly-report", LaunchMode: "api",
	})
	want := `[SCHED] executor=worker-a worker=11111111-2222-3333-4444-555555555555 job="nightly-report" build=aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee trigger=manual`
	if got != want {
		t.Fatalf("header\n got: %s\nwant: %s", got, want)
	}
}

func TestFormatBuildLogHeaderCron(t *testing.T) {
	workerID := uuid.New()
	runID := uuid.New()
	got := formatBuildLogHeader(workerID, "worker-b", claimedRun{
		RunID: runID, JobName: "cron-job", LaunchMode: "cron",
	})
	if !strings.Contains(got, "trigger=cron") {
		t.Fatalf("cron header missing trigger: %s", got)
	}
}

func TestFormatBuildLogHeaderDependency(t *testing.T) {
	workerID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	runID := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	upstreamID := uuid.MustParse("99999999-8888-4777-8666-555555555555")
	upstreamJob := "build-image"
	got := formatBuildLogHeader(workerID, "", claimedRun{
		RunID: runID, JobName: "deploy", LaunchMode: "dependency",
		TriggeredByRunID: &upstreamID, TriggeredByJobName: &upstreamJob,
	})
	// Empty host falls back to the worker ID so the executor is never blank.
	if !strings.HasPrefix(got, "[SCHED] executor=11111111-2222-3333-4444-555555555555 worker=11111111-2222-3333-4444-555555555555 ") {
		t.Fatalf("dependency header executor fallback missing: %s", got)
	}
	if !strings.Contains(got, `trigger=dependency:"build-image"#99999999-8888-4777-8666-555555555555`) {
		t.Fatalf("dependency header missing upstream build: %s", got)
	}
	if !strings.Contains(got, `job="deploy" build=aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee`) {
		t.Fatalf("dependency header missing current build: %s", got)
	}
}

func TestFormatBuildLogHeaderDependencyWithoutName(t *testing.T) {
	upstreamID := uuid.New()
	got := formatBuildLogHeader(uuid.New(), "worker-c", claimedRun{
		RunID: uuid.New(), JobName: "deploy", LaunchMode: "dependency",
		TriggeredByRunID: &upstreamID,
	})
	if !strings.Contains(got, `trigger=dependency:"unknown"#`+upstreamID.String()) {
		t.Fatalf("missing upstream fallback: %s", got)
	}
}
