package domain

import (
	"fmt"
	"time"
)

// TimeHourLayout is the external time_hour format: YYYYMMDDHHmm, minute precision.
const TimeHourLayout = "200601021504"

// ValidateTimeHour validates the fixed 12-digit time_hour parameter.
func ValidateTimeHour(value string) error {
	if len(value) != 12 {
		return fmt.Errorf("time_hour must be 12 digits (YYYYMMDDHHmm)")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return fmt.Errorf("time_hour must be 12 digits (YYYYMMDDHHmm)")
		}
	}
	if _, err := time.Parse(TimeHourLayout, value); err != nil {
		return fmt.Errorf("time_hour must be a valid YYYYMMDDHHmm: %w", err)
	}
	return nil
}

// TimeHourAt formats an instant in the job timezone as time_hour.
func TimeHourAt(at time.Time, timezone string) (string, error) {
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return "", fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	return at.In(loc).Format(TimeHourLayout), nil
}
