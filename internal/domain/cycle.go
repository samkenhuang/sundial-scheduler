package domain

import (
	"fmt"
	"strings"
	"time"
)

// Supported cycle formats keep cycle keys lexical and explainable. Explicit
// API backfills may pass any non-empty key; cron/API defaults use these.
var supportedCycleFormats = map[string]struct{}{
	"2006":          {},
	"2006-01":       {},
	"2006-01-02":    {},
	"2006-01-02T15": {},
}

func ValidateCycleFormat(format string) error {
	format = strings.TrimSpace(format)
	if format == "" {
		return fmt.Errorf("cycleFormat is required")
	}
	if _, ok := supportedCycleFormats[format]; !ok {
		return fmt.Errorf("unsupported cycleFormat %q (use 2006, 2006-01, 2006-01-02, 2006-01-02T15)", format)
	}
	return nil
}

// CycleKey truncates an instant to the job's cycle. Timezone matters: a daily
// job in Asia/Shanghai must not cross the boundary at UTC midnight.
func CycleKey(format, timezone string, at time.Time) (string, error) {
	if err := ValidateCycleFormat(format); err != nil {
		return "", err
	}
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return "", fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	return at.In(loc).Format(format), nil
}
