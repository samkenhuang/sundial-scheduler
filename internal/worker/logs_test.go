package worker

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"scheduler-platform/internal/config"
)

func writeLog(t *testing.T, path string, body string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureAndCleanupLogs(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	writeLog(t, filepath.Join(root, "runs", "a", "attempt-0.log"), "old", old)
	writeLog(t, filepath.Join(root, "runs", "b", "attempt-0.log"), "newer", time.Now())
	used, err := measureLogUsage(root)
	if err != nil || used != int64(len("old")+len("newer")) {
		t.Fatalf("used=%d err=%v", used, err)
	}
	deleted, bytes, err := cleanupLogs(root, time.Now().Add(-24*time.Hour))
	if err != nil || deleted != 1 || bytes != 3 {
		t.Fatalf("deleted=%d bytes=%d err=%v", deleted, bytes, err)
	}
	used, _ = measureLogUsage(root)
	if used != int64(len("newer")) {
		t.Fatalf("used after cleanup=%d", used)
	}
}

func TestIncrementalLogUsageTracksDeltas(t *testing.T) {
	w := &Worker{log: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: config.Config{
		LogQuotaBytes: 1000, LogHighWater: 0.8, LogLowWater: 0.5,
	}}
	w.addLogUsage(500)
	if w.logUsed.Load() != 500 || w.logHigh.Load() {
		t.Fatalf("used=%d high=%v", w.logUsed.Load(), w.logHigh.Load())
	}
	w.addLogUsage(350)
	if !w.logHigh.Load() {
		t.Fatal("expected high watermark after incremental add")
	}
	w.addLogUsage(-600)
	if w.logUsed.Load() != 250 || w.logHigh.Load() {
		t.Fatalf("after cleanup used=%d high=%v", w.logUsed.Load(), w.logHigh.Load())
	}
	w.addLogUsage(-10000)
	if w.logUsed.Load() != 0 {
		t.Fatalf("usage underflow=%d", w.logUsed.Load())
	}
}

func TestTrackLogFileUsesFileDelta(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runs", "a", "attempt-0.log")
	writeLog(t, path, "12345", time.Now())
	w := &Worker{log: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: config.Config{LogRoot: root}}
	w.trackLogFile(path, 2)
	if w.logUsed.Load() != 3 {
		t.Fatalf("used=%d", w.logUsed.Load())
	}
	w.trackLogFile(path, 5)
	if w.logUsed.Load() != 3 {
		t.Fatalf("no-growth delta leaked: used=%d", w.logUsed.Load())
	}
}

func TestLogHighWatermarkStopsAndClears(t *testing.T) {
	root := t.TempDir()
	writeLog(t, filepath.Join(root, "runs", "a", "attempt-0.log"), string(make([]byte, 80)), time.Now())
	w := &Worker{log: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: config.Config{
		LogRoot: root, LogQuotaBytes: 100, LogHighWater: 0.5, LogLowWater: 0.3,
	}}
	w.refreshLogUsage()
	if !w.logHigh.Load() {
		t.Fatal("expected high watermark")
	}
	if _, _, err := cleanupLogs(root, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	w.refreshLogUsage()
	if w.logHigh.Load() {
		t.Fatal("expected claim to resume after cleanup")
	}
}
