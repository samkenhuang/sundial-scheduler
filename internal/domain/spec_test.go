package domain

import (
	"testing"
	"time"
)

func TestNormalizeAndValidate(t *testing.T) {
	spec := JobSpec{Command: []string{"echo hi"}, Labels: []string{" linux ", "linux", "cpu=8"}}
	spec.Normalize()
	if spec.APIVersion != "sched/v1" || spec.TimeoutSeconds != 3600 {
		t.Fatalf("unexpected defaults: %+v", spec)
	}
	if len(spec.Labels) != 2 || spec.Labels[0] != "cpu=8" || spec.Labels[1] != "linux" {
		t.Fatalf("labels not normalized: %v", spec.Labels)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if err := (JobSpec{}).Validate(); err == nil {
		t.Fatal("empty command accepted")
	}
}

func TestNextFire(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	next, err := NextFire("@every 2s", "UTC", now)
	if err != nil {
		t.Fatalf("next fire: %v", err)
	}
	if !next.Equal(now.Add(2 * time.Second)) {
		t.Fatalf("next=%s", next)
	}
	if _, err := NextFire("not-a-cron", "UTC", now); err == nil {
		t.Fatal("invalid cron accepted")
	}
}

func TestRunTransitions(t *testing.T) {
	if !CanTransition(RunQueued, RunRunning) || !CanTransition(RunRunning, RunSucceeded) {
		t.Fatal("expected valid transitions rejected")
	}
	if CanTransition(RunSucceeded, RunRunning) || CanTransition(RunQueued, RunSucceeded) {
		t.Fatal("invalid transition accepted")
	}
	if !RunSucceeded.Terminal() || RunQueued.Terminal() {
		t.Fatal("terminal state classification wrong")
	}
}
