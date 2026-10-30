package presence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Record is ephemeral worker liveness state. It is deliberately not durable:
// losing presence only affects UI/offline reporting, never run ownership.
type Record struct {
	WorkerID     uuid.UUID `json:"workerId"`
	LastSeen     time.Time `json:"lastSeen"`
	LogUsedBytes int64     `json:"logUsedBytes"`
}

// ErrLeaderLockUnavailable means no shared Redis client is configured or the
// client could not serve the leader lock operation.
var ErrLeaderLockUnavailable = errors.New("leader lock unavailable")

type Store interface {
	Touch(ctx context.Context, rec Record) error
	Get(ctx context.Context, id uuid.UUID) (Record, bool, error)
	List(ctx context.Context) ([]Record, error)
	Available() bool
	Close() error
}

// Local is a process-local TTL cache. It is the fallback when Redis is absent
// or unavailable. In a multi-master deployment a local record is only what this
// Gateway has seen, so presence is best-effort by definition.
type Local struct {
	mu   sync.Mutex
	ttl  time.Duration
	data map[uuid.UUID]entry
}

type entry struct {
	rec     Record
	expires time.Time
}

func NewLocal(ttl time.Duration) *Local {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	return &Local{ttl: ttl, data: map[uuid.UUID]entry{}}
}

func (l *Local) Touch(_ context.Context, rec Record) error {
	if rec.WorkerID == uuid.Nil {
		return fmt.Errorf("presence: worker id is required")
	}
	if rec.LastSeen.IsZero() {
		rec.LastSeen = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data[rec.WorkerID] = entry{rec: rec, expires: time.Now().Add(l.ttl)}
	return nil
}

func (l *Local) Get(_ context.Context, id uuid.UUID) (Record, bool, error) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.data[id]
	if !ok {
		return Record{}, false, nil
	}
	if now.After(e.expires) {
		delete(l.data, id)
		return Record{}, false, nil
	}
	return e.rec, true, nil
}

func (l *Local) List(_ context.Context) ([]Record, error) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Record, 0, len(l.data))
	for id, e := range l.data {
		if now.After(e.expires) {
			delete(l.data, id)
			continue
		}
		out = append(out, e.rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out, nil
}

func (l *Local) Available() bool { return true }
func (l *Local) Close() error    { return nil }

// Redis is the shared presence store. Every entry has a TTL; there is no
// heartbeat row in PostgreSQL and no background delete required.
type Redis struct {
	cli       *redis.Client
	ttl       time.Duration
	prefix    string
	available atomic.Bool
	log       *slog.Logger
}

func NewRedis(url string, ttl time.Duration, log *slog.Logger) (*Redis, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	// Presence must never add seconds of latency to a heartbeat when Redis is
	// down. Keep every Redis operation short and fall back to local memory.
	opts.DialTimeout = 500 * time.Millisecond
	opts.ReadTimeout = 500 * time.Millisecond
	opts.WriteTimeout = 500 * time.Millisecond
	opts.PoolTimeout = 500 * time.Millisecond
	opts.MaxRetries = 0
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	r := &Redis{cli: redis.NewClient(opts), ttl: ttl, prefix: "sched:presence:worker:", log: log}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.cli.Ping(ctx).Err(); err != nil {
		// Keep the client: a later heartbeat/claim write can recover Redis
		// without restarting the Gateway. Until then, reads use local fallback.
		r.available.Store(false)
		r.log.Warn("redis presence ping failed; local fallback until recovery", "error", err)
		return r, nil
	}
	r.available.Store(true)
	return r, nil
}

func (r *Redis) key(id uuid.UUID) string { return r.prefix + id.String() }

func (r *Redis) Touch(ctx context.Context, rec Record) error {
	if rec.WorkerID == uuid.Nil {
		return fmt.Errorf("presence: worker id is required")
	}
	if rec.LastSeen.IsZero() {
		rec.LastSeen = time.Now().UTC()
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := r.cli.Set(ctx, r.key(rec.WorkerID), body, r.ttl).Err(); err != nil {
		r.available.Store(false)
		return err
	}
	r.available.Store(true)
	return nil
}

func (r *Redis) Get(ctx context.Context, id uuid.UUID) (Record, bool, error) {
	body, err := r.cli.Get(ctx, r.key(id)).Bytes()
	if err == redis.Nil {
		r.available.Store(true)
		return Record{}, false, nil
	}
	if err != nil {
		r.available.Store(false)
		return Record{}, false, err
	}
	var rec Record
	if err := json.Unmarshal(body, &rec); err != nil {
		return Record{}, false, fmt.Errorf("decode presence: %w", err)
	}
	r.available.Store(true)
	return rec, true, nil
}

func (r *Redis) List(ctx context.Context) ([]Record, error) {
	keys := make([]string, 0, 64)
	iter := r.cli.Scan(ctx, 0, r.prefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		r.available.Store(false)
		return nil, err
	}
	if len(keys) == 0 {
		r.available.Store(true)
		return []Record{}, nil
	}
	vals, err := r.cli.MGet(ctx, keys...).Result()
	if err != nil {
		r.available.Store(false)
		return nil, err
	}
	out := make([]Record, 0, len(vals))
	for _, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(s), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	r.available.Store(true)
	return out, nil
}

func (r *Redis) Available() bool { return r.available.Load() }

func (r *Redis) Close() error {
	r.available.Store(false)
	return r.cli.Close()
}

const leaderLockKey = "sched:leader:scheduler"

var renewLeaderScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("pexpire", KEYS[1], ARGV[2])
end
return 0`)

var releaseLeaderScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
end
return 0`)

// TryLeaderLock takes the shared leader lock with a TTL. The token is the
// scheduler node id; renewal and release are token-fenced.
func (r *Redis) TryLeaderLock(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	ok, err := r.cli.SetNX(ctx, leaderLockKey, token, ttl).Result()
	if err != nil {
		r.available.Store(false)
		return false, err
	}
	r.available.Store(true)
	return ok, nil
}

func (r *Redis) RenewLeaderLock(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	n, err := renewLeaderScript.Run(ctx, r.cli, []string{leaderLockKey}, token, ttl.Milliseconds()).Int()
	if err != nil {
		r.available.Store(false)
		return false, err
	}
	r.available.Store(true)
	return n == 1, nil
}

func (r *Redis) ReleaseLeaderLock(ctx context.Context, token string) error {
	_, err := releaseLeaderScript.Run(ctx, r.cli, []string{leaderLockKey}, token).Int()
	return err
}

func (f *Fallback) TryLeaderLock(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	r, ok := f.primary.(*Redis)
	if !ok || r == nil {
		return false, ErrLeaderLockUnavailable
	}
	return r.TryLeaderLock(ctx, token, ttl)
}

func (f *Fallback) RenewLeaderLock(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	r, ok := f.primary.(*Redis)
	if !ok || r == nil {
		return false, ErrLeaderLockUnavailable
	}
	return r.RenewLeaderLock(ctx, token, ttl)
}

func (f *Fallback) ReleaseLeaderLock(ctx context.Context, token string) error {
	r, ok := f.primary.(*Redis)
	if !ok || r == nil {
		return ErrLeaderLockUnavailable
	}
	return r.ReleaseLeaderLock(ctx, token)
}

// Fallback writes local first so a Redis outage cannot break heartbeat
// ingestion. Reads use Redis while it is healthy, then fall back to local data.
type Fallback struct {
	primary  Store
	local    *Local
	log      *slog.Logger
	downOnce atomic.Bool
	retryAt  atomic.Int64 // UnixNano; avoid probing Redis on every heartbeat while it is down
}

func New(redisURL string, ttl time.Duration, log *slog.Logger) *Fallback {
	f := &Fallback{local: NewLocal(ttl), log: log}
	if redisURL != "" {
		r, err := NewRedis(redisURL, ttl, log)
		if err != nil {
			log.Warn("redis presence unavailable; using local fallback", "error", err)
		} else {
			f.primary = r
			log.Info("redis presence enabled")
		}
	} else {
		log.Info("redis url not configured; using local presence cache")
	}
	return f
}

func (f *Fallback) Touch(ctx context.Context, rec Record) error {
	if err := f.local.Touch(ctx, rec); err != nil {
		return err
	}
	if f.primary == nil {
		return nil
	}
	if !f.primary.Available() && time.Now().UnixNano() < f.retryAt.Load() {
		return nil // local write is enough until the next probe
	}
	// Bound the shared-cache attempt so a dead Redis cannot slow heartbeats.
	writeCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	if err := f.primary.Touch(writeCtx, rec); err != nil {
		f.retryAt.Store(time.Now().Add(5 * time.Second).UnixNano())
		if f.downOnce.CompareAndSwap(false, true) {
			f.log.Warn("redis presence write failed; local fallback active", "error", err)
		}
		return nil // local write already succeeded
	}
	if f.downOnce.CompareAndSwap(true, false) {
		f.log.Info("redis presence recovered")
	}
	return nil
}

func (f *Fallback) Get(ctx context.Context, id uuid.UUID) (Record, bool, error) {
	if f.primary != nil {
		// Also probe while marked unavailable, with backoff. The Scheduler is a
		// read-only presence consumer; without this it would never notice that
		// Redis recovered and would stay on DB fallback forever.
		if f.primary.Available() || time.Now().UnixNano() >= f.retryAt.Load() {
			readCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
			rec, ok, err := f.primary.Get(readCtx, id)
			cancel()
			if err == nil {
				f.retryAt.Store(0)
				if f.downOnce.CompareAndSwap(true, false) {
					f.log.Info("redis presence recovered")
				}
				return rec, ok, nil
			}
			f.retryAt.Store(time.Now().Add(5 * time.Second).UnixNano())
			if f.downOnce.CompareAndSwap(false, true) {
				f.log.Warn("redis presence read failed; local fallback active", "error", err)
			}
		}
	}
	return f.local.Get(ctx, id)
}

func (f *Fallback) List(ctx context.Context) ([]Record, error) {
	if f.primary != nil {
		if f.primary.Available() || time.Now().UnixNano() >= f.retryAt.Load() {
			readCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
			recs, err := f.primary.List(readCtx)
			cancel()
			if err == nil {
				f.retryAt.Store(0)
				if f.downOnce.CompareAndSwap(true, false) {
					f.log.Info("redis presence recovered")
				}
				return recs, nil
			}
			f.retryAt.Store(time.Now().Add(5 * time.Second).UnixNano())
			if f.downOnce.CompareAndSwap(false, true) {
				f.log.Warn("redis presence list failed; local fallback active", "error", err)
			}
		}
	}
	return f.local.List(ctx)
}

func (f *Fallback) Available() bool {
	return f.primary != nil && f.primary.Available()
}

func (f *Fallback) Close() error {
	if f.primary != nil {
		return f.primary.Close()
	}
	return nil
}
