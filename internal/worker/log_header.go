package worker

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// formatBuildLogHeader renders the stable first log line for a build.
//
// Format:
//
//	[SCHED] executor=<host> worker=<id> job="<name>" build=<run-id> trigger=<source>
//
// A dependency trigger includes the actual upstream build that completed and
// caused this run to be created:
//
//	trigger=dependency:"<upstream-job>"#<upstream-run-id>
//
// Manual/cron runs have no upstream build, so their trigger is rendered as
// manual / cron. The full run UUIDs keep the line copy-pasteable for tracing
// even when the UI shows only the short prefix.
func formatBuildLogHeader(workerID uuid.UUID, host string, run claimedRun) string {
	executor := strings.TrimSpace(host)
	if executor == "" {
		executor = workerID.String()
	}
	return fmt.Sprintf("[SCHED] executor=%s worker=%s job=%q build=%s trigger=%s",
		executor, workerID.String(), run.JobName, run.RunID.String(), triggerSource(run))
}

func triggerSource(run claimedRun) string {
	if run.TriggeredByRunID != nil {
		upstreamJob := ""
		if run.TriggeredByJobName != nil {
			upstreamJob = strings.TrimSpace(*run.TriggeredByJobName)
		}
		if upstreamJob == "" {
			upstreamJob = "unknown"
		}
		return fmt.Sprintf("dependency:%q#%s", upstreamJob, run.TriggeredByRunID.String())
	}
	switch run.LaunchMode {
	case "api":
		return "manual"
	case "cron":
		return "cron"
	default:
		if run.LaunchMode == "" {
			return "unknown"
		}
		return run.LaunchMode
	}
}
