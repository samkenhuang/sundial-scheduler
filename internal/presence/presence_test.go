package presence

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLocalTTL(t *testing.T) {
	ctx := context.Background()
	l := NewLocal(20 * time.Millisecond)
	id := uuid.New()
	if err := l.Touch(ctx, Record{WorkerID: id, LogUsedBytes: 7}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := l.Get(ctx, id)
	if err != nil || !ok || rec.LogUsedBytes != 7 {
		t.Fatalf("get rec=%+v ok=%v err=%v", rec, ok, err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, ok, _ := l.Get(ctx, id); ok {
		t.Fatal("presence should expire")
	}
}

func TestRedisPresenceAndTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	r, err := NewRedis("redis://"+mr.Addr(), 2*time.Second, discard())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	id := uuid.New()
	if err := r.Touch(ctx, Record{WorkerID: id, LogUsedBytes: 42}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := r.Get(ctx, id)
	if err != nil || !ok || rec.LogUsedBytes != 42 {
		t.Fatalf("get rec=%+v ok=%v err=%v", rec, ok, err)
	}
	recs, err := r.List(ctx)
	if err != nil || len(recs) != 1 || recs[0].WorkerID != id {
		t.Fatalf("list recs=%+v err=%v", recs, err)
	}
	mr.FastForward(3 * time.Second)
	if _, ok, _ := r.Get(ctx, id); ok {
		t.Fatal("redis presence should expire")
	}
}

func TestRedisLeaderLockFencing(t *testing.T) {
	mr := miniredis.RunT(t)
	r, err := NewRedis("redis://"+mr.Addr(), time.Minute, discard())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	if ok, err := r.TryLeaderLock(ctx, "node-a", 5*time.Second); err != nil || !ok {
		t.Fatalf("node-a acquire: %v %v", ok, err)
	}
	if ok, err := r.TryLeaderLock(ctx, "node-b", 5*time.Second); err != nil || ok {
		t.Fatalf("node-b must not acquire: %v %v", ok, err)
	}
	if ok, err := r.RenewLeaderLock(ctx, "node-b", 5*time.Second); err != nil || ok {
		t.Fatalf("node-b must not renew: %v %v", ok, err)
	}
	if ok, err := r.RenewLeaderLock(ctx, "node-a", 5*time.Second); err != nil || !ok {
		t.Fatalf("node-a renew: %v %v", ok, err)
	}
	mr.FastForward(6 * time.Second)
	if ok, err := r.TryLeaderLock(ctx, "node-b", 5*time.Second); err != nil || !ok {
		t.Fatalf("node-b acquire after expiry: %v %v", ok, err)
	}
	if err := r.ReleaseLeaderLock(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.RenewLeaderLock(ctx, "node-b", 5*time.Second); err != nil || !ok {
		t.Fatalf("stale node-a release must not drop node-b: %v %v", ok, err)
	}
}

func TestFallbackLeaderLockWithoutRedis(t *testing.T) {
	f := New("", time.Minute, discard())
	defer f.Close()
	if _, err := f.TryLeaderLock(context.Background(), "node-a", time.Second); !errors.Is(err, ErrLeaderLockUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
}

func TestFallbackUsesLocalWhenRedisUnavailable(t *testing.T) {
	ctx := context.Background()
	f := New("redis://127.0.0.1:1", time.Minute, discard())
	defer f.Close()
	if f.Available() {
		t.Fatal("unavailable redis should not be primary")
	}
	id := uuid.New()
	if err := f.Touch(ctx, Record{WorkerID: id, LogUsedBytes: 9}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := f.Get(ctx, id)
	if err != nil || !ok || rec.LogUsedBytes != 9 {
		t.Fatalf("fallback get rec=%+v ok=%v err=%v", rec, ok, err)
	}
	recs, err := f.List(ctx)
	if err != nil || len(recs) != 1 {
		t.Fatalf("fallback list recs=%+v err=%v", recs, err)
	}
}

func TestFallbackUsesRedisWhenAvailable(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()
	f := New("redis://"+mr.Addr(), time.Minute, discard())
	defer f.Close()
	if !f.Available() {
		t.Fatal("redis should be available")
	}
	id := uuid.New()
	if err := f.Touch(ctx, Record{WorkerID: id, LogUsedBytes: 11}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := f.Get(ctx, id)
	if err != nil || !ok || rec.LogUsedBytes != 11 {
		t.Fatalf("redis get rec=%+v ok=%v err=%v", rec, ok, err)
	}
}
