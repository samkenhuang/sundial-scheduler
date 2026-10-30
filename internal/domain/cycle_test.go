package domain

import (
	"testing"
	"time"
)

func TestCycleKeyUsesJobTimezone(t *testing.T) {
	at := time.Date(2026, 10, 24, 16, 30, 0, 0, time.UTC) // 2026-10-25 00:30 Shanghai
	got, err := CycleKey("2006-01-02", "Asia/Shanghai", at)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-10-25" {
		t.Fatalf("cycle=%q, want 2026-10-25", got)
	}
	got, err = CycleKey("2006-01-02", "UTC", at)
	if err != nil || got != "2026-10-24" {
		t.Fatalf("utc cycle=%q err=%v", got, err)
	}
}

func TestCycleKeyFormats(t *testing.T) {
	at := time.Date(2026, 10, 24, 16, 30, 0, 0, time.UTC)
	cases := map[string]string{
		"2006":          "2026",
		"2006-01":       "2026-10",
		"2006-01-02":    "2026-10-24",
		"2006-01-02T15": "2026-10-24T16",
	}
	for format, want := range cases {
		got, err := CycleKey(format, "UTC", at)
		if err != nil || got != want {
			t.Fatalf("format %q: got %q err=%v want %q", format, got, err, want)
		}
	}
	if err := ValidateCycleFormat("15:04"); err == nil {
		t.Fatal("expected unsupported format to fail")
	}
}
