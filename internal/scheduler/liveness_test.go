package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"scheduler-platform/internal/presence"
)

type fakePresence struct {
	available bool
	rec       presence.Record
	ok        bool
}

func (f *fakePresence) Touch(context.Context, presence.Record) error { return nil }
func (f *fakePresence) Get(context.Context, uuid.UUID) (presence.Record, bool, error) {
	return f.rec, f.ok, nil
}
func (f *fakePresence) List(context.Context) ([]presence.Record, error) {
	if f.ok {
		return []presence.Record{f.rec}, nil
	}
	return nil, nil
}
func (f *fakePresence) Available() bool { return f.available }
func (f *fakePresence) Close() error    { return nil }

func testLiveness(t *testing.T, available bool) (*livenessSource, *fakePresence) {
	t.Helper()
	fp := &fakePresence{available: available}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newLivenessSource(fp, 90*time.Second, 90*time.Second, 60*time.Second, log), fp
}

func TestLivenessPrefersFreshRedis(t *testing.T) {
	l, fp := testLiveness(t, true)
	now := time.Now()
	fp.rec = presence.Record{WorkerID: uuid.New(), LastSeen: now}
	fp.ok = true
	alive, source := l.Alive(context.Background(), uuid.New(), false)
	if !alive || source != "redis" {
		t.Fatalf("alive=%v source=%s", alive, source)
	}
}

func TestLivenessFallsBackToFreshDB(t *testing.T) {
	l, fp := testLiveness(t, false)
	fp.ok = false
	alive, source := l.Alive(context.Background(), uuid.New(), true)
	if !alive || source != "db" {
		t.Fatalf("alive=%v source=%s", alive, source)
	}
}

func TestLivenessDBFallbackGraceThenDead(t *testing.T) {
	l, fp := testLiveness(t, false)
	fp.ok = false
	alive, source := l.Alive(context.Background(), uuid.New(), false)
	if !alive || source != "fallback-grace" {
		t.Fatalf("initial fallback: alive=%v source=%s", alive, source)
	}
	l.fallbackSince = time.Now().Add(-2 * time.Minute)
	alive, source = l.Alive(context.Background(), uuid.New(), false)
	if alive || source != "offline" {
		t.Fatalf("after grace: alive=%v source=%s", alive, source)
	}
}

func TestLivenessRedisWarmupThenDead(t *testing.T) {
	l, fp := testLiveness(t, true)
	fp.ok = false
	alive, source := l.Alive(context.Background(), uuid.New(), false)
	if !alive || source != "redis-warmup" {
		t.Fatalf("initial warmup: alive=%v source=%s", alive, source)
	}
	l.warmupUntil = time.Now().Add(-time.Second)
	alive, source = l.Alive(context.Background(), uuid.New(), false)
	if alive || source != "offline" {
		t.Fatalf("after warmup: alive=%v source=%s", alive, source)
	}
	fp.ok = true
	fp.rec = presence.Record{WorkerID: uuid.New(), LastSeen: time.Now()}
	alive, source = l.Alive(context.Background(), uuid.New(), false)
	if !alive || source != "redis" {
		t.Fatalf("recovered redis: alive=%v source=%s", alive, source)
	}
}

func TestLivenessRecoversFromDBToRedis(t *testing.T) {
	l, fp := testLiveness(t, false)
	fp.ok = false
	alive, source := l.Alive(context.Background(), uuid.New(), false)
	if !alive || source != "fallback-grace" {
		t.Fatalf("down: alive=%v source=%s", alive, source)
	}
	fp.available = true
	alive, source = l.Alive(context.Background(), uuid.New(), true)
	if !alive || source != "redis-warmup" {
		t.Fatalf("warmup/db: alive=%v source=%s", alive, source)
	}
	fp.ok = true
	fp.rec = presence.Record{WorkerID: uuid.New(), LastSeen: time.Now()}
	alive, source = l.Alive(context.Background(), uuid.New(), false)
	if !alive || source != "redis" {
		t.Fatalf("recovered: alive=%v source=%s", alive, source)
	}
}
