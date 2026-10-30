package domain

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type RunState string

const (
	RunPending   RunState = "PENDING"
	RunQueued    RunState = "QUEUED"
	RunRunning   RunState = "RUNNING"
	RunSucceeded RunState = "SUCCEEDED"
	RunFailed    RunState = "FAILED"
	RunCanceled  RunState = "CANCELED"
	RunTimedOut  RunState = "TIMED_OUT"
)

func (s RunState) Terminal() bool {
	switch s {
	case RunSucceeded, RunFailed, RunCanceled, RunTimedOut:
		return true
	default:
		return false
	}
}

// CanTransition is the single state-machine authority used by tests and by
// callers before issuing a CAS update.
func CanTransition(from, to RunState) bool {
	switch from {
	case RunPending:
		return to == RunQueued || to == RunCanceled
	case RunQueued:
		return to == RunRunning || to == RunCanceled
	case RunRunning:
		switch to {
		case RunSucceeded, RunFailed, RunTimedOut, RunCanceled:
			return true
		}
	}
	return false
}

type Run struct {
	ID                      uuid.UUID  `json:"id"`
	JobID                   uuid.UUID  `json:"jobId"`
	JobVersionID            uuid.UUID  `json:"jobVersionId"`
	LaunchMode              string     `json:"launchMode"`
	State                   RunState   `json:"state"`
	Priority                int        `json:"priority"`
	RequiredLabels          []byte     `json:"-"`
	TimeHour                string     `json:"timeHour"`
	CycleKey                *string    `json:"cycleKey,omitempty"`
	Attempt                 int        `json:"attempt"`
	IdempotencyKey          *string    `json:"idempotencyKey,omitempty"`
	DedupKey                *string    `json:"dedupKey,omitempty"`
	ConcurrencyKey          *string    `json:"concurrencyKey,omitempty"`
	Inputs                  []byte     `json:"-"`
	CommandSnapshot         []byte     `json:"-"`
	ScheduledAt             *time.Time `json:"scheduledAt,omitempty"`
	EnqueuedAt              *time.Time `json:"enqueuedAt,omitempty"`
	AvailableAt             time.Time  `json:"availableAt"`
	LeaseOwner              *uuid.UUID `json:"leaseOwner,omitempty"`
	LeaseToken              *uuid.UUID `json:"leaseToken,omitempty"`
	StartedAt               *time.Time `json:"startedAt,omitempty"`
	FinishedAt              *time.Time `json:"finishedAt,omitempty"`
	CancelRequestedAt       *time.Time `json:"cancelRequestedAt,omitempty"`
	Error                   *string    `json:"error,omitempty"`
	DependenciesProcessedAt *time.Time `json:"dependenciesProcessedAt,omitempty"`
	ArchivedAt              *time.Time `json:"archivedAt,omitempty"`
	Version                 int64      `json:"version"`
	CreatedAt               time.Time  `json:"createdAt"`
}

func (r Run) Labels() ([]string, error) {
	return decodeLabels(r.RequiredLabels)
}

func (r Run) Spec() (JobSpec, error) {
	var spec JobSpec
	if err := json.Unmarshal(r.CommandSnapshot, &spec); err != nil {
		return spec, fmt.Errorf("decode command_snapshot: %w", err)
	}
	return spec, nil
}

type Job struct {
	ID               uuid.UUID  `json:"id"`
	ProjectID        *uuid.UUID `json:"projectId,omitempty"`
	Name             string     `json:"name"`
	CurrentVersionID *uuid.UUID `json:"currentVersionId,omitempty"`
	Cron             *string    `json:"cron,omitempty"`
	Timezone         string     `json:"timezone"`
	ScheduleEnabled  bool       `json:"scheduleEnabled"`
	NextFireAt       *time.Time `json:"nextFireAt,omitempty"`
	LastFireAt       *time.Time `json:"lastFireAt,omitempty"`
	CreatedBy        string     `json:"createdBy"`
	CreatedAt        time.Time  `json:"createdAt"`
	ArchivedAt       *time.Time `json:"archivedAt,omitempty"`
	CycleFormat      string     `json:"cycleFormat"`
	LogRetentionRuns int        `json:"logRetentionRuns"`
	LogRetentionDays int        `json:"logRetentionDays"`
}

type JobVersion struct {
	ID        uuid.UUID `json:"id"`
	JobID     uuid.UUID `json:"jobId"`
	Version   int       `json:"version"`
	Spec      []byte    `json:"-"`
	SpecHash  string    `json:"specHash"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type Worker struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	Labels           []byte    `json:"-"`
	ConcurrencyLimit int       `json:"concurrencyLimit"`
	Status           string    `json:"status"`
	Hostname         string    `json:"hostname"`
	Version          string    `json:"version"`
	LogUsedBytes     int64     `json:"logUsedBytes"`
	LogQuotaBytes    int64     `json:"logQuotaBytes"`
	RegisteredAt     time.Time `json:"registeredAt"`
	LastHeartbeatAt  time.Time `json:"lastHeartbeatAt"`
	Draining         bool      `json:"draining"`
}

func (w Worker) LabelList() ([]string, error) {
	return decodeLabels(w.Labels)
}

type RunAttempt struct {
	ID         uuid.UUID
	RunID      uuid.UUID
	Attempt    int
	WorkerID   *uuid.UUID
	State      string
	ExitCode   *int
	LogPrefix  *string
	Error      *string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

type WorkerCommand struct {
	ID          uuid.UUID       `json:"id"`
	WorkerID    uuid.UUID       `json:"workerId"`
	RunID       *uuid.UUID      `json:"runId,omitempty"`
	Attempt     *int            `json:"attempt,omitempty"`
	Seq         *int            `json:"seq,omitempty"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	State       string          `json:"state"`
	Result      json.RawMessage `json:"result,omitempty"`
	LastError   *string         `json:"lastError,omitempty"`
	ExpiresAt   time.Time       `json:"expiresAt"`
	DeliveredAt *time.Time      `json:"deliveredAt,omitempty"`
	FinishedAt  *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

const (
	CmdFetchLog   = "fetch_log"
	CmdArchiveLog = "archive_log"
	CmdLogCleanup = "log_cleanup"
	CmdCancel     = "cancel"
)

type LogManifest struct {
	RunID           uuid.UUID
	Attempt         int
	Seq             int
	StorageType     string
	WorkerID        *uuid.UUID
	LocalPath       *string
	ObjectKey       *string
	ArchiveURI      *string
	ArchiveState    string
	ArchivedAt      *time.Time
	ArchiveError    *string
	Bytes           int64
	CompressedBytes int64
	SHA256          *string
	Truncated       bool
	CreatedAt       time.Time
}

func decodeLabels(raw []byte) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var labels []string
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil, fmt.Errorf("decode labels: %w", err)
	}
	return labels, nil
}

type Project struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}
