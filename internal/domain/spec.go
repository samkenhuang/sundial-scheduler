package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ConcurrencySpec struct {
	Key    string `json:"key,omitempty"`
	Policy string `json:"policy,omitempty"` // allow | serial | replace | cancel
}

type CycleSpec struct {
	Format string `json:"format,omitempty"`
	Source string `json:"source,omitempty"`
}

type JobSpec struct {
	APIVersion     string            `json:"apiVersion,omitempty"`
	Command        []string          `json:"command"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Priority       int               `json:"priority,omitempty"`
	Labels         []string          `json:"labels,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
	Concurrency    ConcurrencySpec   `json:"concurrency,omitempty"`
	Cycle          CycleSpec         `json:"cycle,omitempty"`
	ArchivePolicy  string            `json:"archivePolicy,omitempty"`
	Outputs        []string          `json:"outputs,omitempty"`
}

func (s *JobSpec) Normalize() {
	if s.APIVersion == "" {
		s.APIVersion = "sched/v1"
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 3600
	}
	if s.Concurrency.Policy == "" {
		s.Concurrency.Policy = "allow"
	}
	if s.ArchivePolicy == "" {
		s.ArchivePolicy = "manual"
	}
	s.Labels = NormalizeLabels(s.Labels)
	if s.Cycle.Format == "" {
		s.Cycle.Format = "2006-01-02"
	}
}

func (s JobSpec) Validate() error {
	if len(s.Command) == 0 || strings.TrimSpace(s.Command[0]) == "" {
		return fmt.Errorf("command must be a non-empty array")
	}
	if s.APIVersion != "sched/v1" {
		return fmt.Errorf("unsupported apiVersion %q", s.APIVersion)
	}
	if s.TimeoutSeconds < 1 || s.TimeoutSeconds > int((7*24*time.Hour)/time.Second) {
		return fmt.Errorf("timeoutSeconds must be between 1 and 604800")
	}
	switch s.Concurrency.Policy {
	case "allow", "serial", "replace", "cancel":
	default:
		return fmt.Errorf("unsupported concurrency.policy %q", s.Concurrency.Policy)
	}
	switch s.ArchivePolicy {
	case "manual", "on_complete", "before_delete":
	default:
		return fmt.Errorf("unsupported archivePolicy %q", s.ArchivePolicy)
	}
	seen := map[string]struct{}{}
	for _, label := range s.Labels {
		if label == "" {
			return fmt.Errorf("labels must not contain empty values")
		}
		if _, ok := seen[label]; ok {
			return fmt.Errorf("duplicate label %q", label)
		}
		seen[label] = struct{}{}
	}
	return nil
}

func (s JobSpec) Hash() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (s JobSpec) Timeout() time.Duration {
	return time.Duration(s.TimeoutSeconds) * time.Second
}

func NormalizeLabels(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, 0, len(labels))
	seen := map[string]struct{}{}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

// LabelsSubset implements the claim rule required_labels ⊆ worker.labels.
func LabelsSubset(required, available []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(available))
	for _, label := range available {
		set[label] = struct{}{}
	}
	for _, label := range required {
		if _, ok := set[label]; !ok {
			return false
		}
	}
	return true
}
