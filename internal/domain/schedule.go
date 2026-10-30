package domain

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// NextFire parses a standard five-field cron expression in the job timezone.
// The returned time is UTC, matching the database convention.
func NextFire(expr, timezone string, after time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse cron %q: %w", expr, err)
	}
	return schedule.Next(after.In(loc)).UTC(), nil
}
