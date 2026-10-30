package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"scheduler-platform/internal/presence"
)

// livenessSource implements the Redis-first Worker liveness decision:
//
//	Redis healthy: present key -> alive; missing after warmup -> dead
//	Redis unavailable: PostgreSQL last_heartbeat_at (fresh) -> alive
//	otherwise, only after a fallback grace / Redis warmup -> dead
//
// Redis is authoritative in the normal path. PostgreSQL is only the fallback
// while Redis is unavailable; Redis resumes as the preferred source after
// recovery.
type livenessSource struct {
	presence presence.Store
	ttl      time.Duration
	grace    time.Duration
	warmup   time.Duration
	log      *slog.Logger

	initialized   bool
	lastAvailable bool
	fallbackSince time.Time
	warmupUntil   time.Time
}

func newLivenessSource(p presence.Store, ttl, grace, warmup time.Duration, log *slog.Logger) *livenessSource {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	if grace <= 0 {
		grace = ttl
	}
	if warmup <= 0 {
		warmup = ttl
	}
	return &livenessSource{
		presence: p, ttl: ttl, grace: grace, warmup: warmup, log: log,
		fallbackSince: time.Now(),
	}
}

func (l *livenessSource) Alive(ctx context.Context, workerID uuid.UUID, dbFresh bool) (bool, string) {
	now := time.Now()
	// Always call Get, even while Redis is marked unavailable: Fallback.Get
	// probes Redis with backoff, which is how the read-only Scheduler notices
	// recovery and returns to the preferred source.
	_, ok, err := l.presence.Get(ctx, workerID)
	if err != nil {
		l.log.Warn("presence get failed; using DB liveness", "worker_id", workerID, "error", err)
	}
	available := l.presence.Available()

	if !l.initialized {
		l.initialized = true
		l.lastAvailable = available
		if available {
			l.warmupUntil = now.Add(l.warmup)
		} else {
			l.fallbackSince = now
		}
	}
	if available != l.lastAvailable {
		if available {
			l.warmupUntil = now.Add(l.warmup)
			l.log.Info("presence redis recovered; entering warmup", "warmup", l.warmup)
		} else {
			l.fallbackSince = now
			l.log.Warn("presence redis unavailable; using DB liveness with grace", "grace", l.grace)
		}
		l.lastAvailable = available
	}

	if available {
		if ok {
			// Redis keys carry a TTL; a present key is alive. Do not compare the
			// LastSeen wall clock across API/Scheduler machines.
			return true, "redis"
		}
		if now.Before(l.warmupUntil) {
			return true, "redis-warmup"
		}
		// Redis is healthy but the key is gone: the Worker is offline. Do not let
		// a stale DB heartbeat override the authoritative Redis presence here.
		return false, "offline"
	}
	if dbFresh {
		return true, "db"
	}
	if now.Sub(l.fallbackSince) < l.grace {
		return true, "fallback-grace"
	}
	return false, "offline"
}
