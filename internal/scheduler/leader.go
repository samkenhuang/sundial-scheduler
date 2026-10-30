package scheduler

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type leaderMode string

const (
	leaderRedis leaderMode = "redis"
	leaderPG    leaderMode = "pg"
)

// syncLeadership ensures this process still owns leadership and otherwise
// attempts to acquire it. Redis is primary; PostgreSQL session advisory lock is
// the fallback when Redis cannot serve the lock. The single-row DB guard fences
// both modes so a Redis-partitioned leader and a fallback leader cannot both act.
func (s *Scheduler) syncLeadership(ctx context.Context, nodeID uuid.UUID) bool {
	if s.leaderActive {
		if s.renewLeadership(ctx, nodeID) {
			return true
		}
		s.resignLeadership(ctx, "lock renewal failed")
		return false
	}
	return s.tryLeadership(ctx, nodeID)
}

func (s *Scheduler) tryLeadership(ctx context.Context, nodeID uuid.UUID) bool {
	lockCtx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	ok, err := s.presence.TryLeaderLock(lockCtx, nodeID.String(), s.cfg.LeaderLockTTL)
	cancel()
	switch {
	case err != nil:
		s.log.Debug("redis leader lock unavailable; trying PostgreSQL fallback", "error", err)
		return s.tryPostgresLeadership(ctx, nodeID)
	case !ok:
		return false
	}
	// We hold the Redis lock, but only the DB guard owner may act. Release the
	// Redis lock immediately if another leader currently owns the guard.
	guardCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	guarded, guardErr := s.st.TryLeaderGuard(guardCtx, nodeID, string(leaderRedis), s.cfg.LeaderGuardTTL)
	cancel()
	if guardErr != nil || !guarded {
		releaseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = s.presence.ReleaseLeaderLock(releaseCtx, nodeID.String())
		cancel()
		if guardErr != nil {
			s.log.Warn("leader guard unavailable", "error", guardErr)
		}
		return false
	}
	s.becomeLeader(leaderRedis, nodeID)
	return true
}

func (s *Scheduler) tryPostgresLeadership(ctx context.Context, nodeID uuid.UUID) bool {
	sessionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	session, ok, err := s.st.AcquireLeaderSession(sessionCtx)
	cancel()
	if err != nil || !ok {
		return false
	}
	guardCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	guarded, guardErr := s.st.TryLeaderGuard(guardCtx, nodeID, string(leaderPG), s.cfg.LeaderGuardTTL)
	cancel()
	if guardErr != nil || !guarded {
		releaseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		session.Release(releaseCtx)
		cancel()
		return false
	}
	s.pgSession = session
	s.becomeLeader(leaderPG, nodeID)
	return true
}

func (s *Scheduler) becomeLeader(mode leaderMode, nodeID uuid.UUID) {
	s.leaderActive = true
	s.leaderMode = mode
	s.leaderSince = time.Now()
	s.log.Info("leader acquired", "node", nodeID, "mode", mode)
}

func (s *Scheduler) renewLeadership(ctx context.Context, nodeID uuid.UUID) bool {
	renewCtx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()
	if s.leaderMode == leaderRedis {
		ok, err := s.presence.RenewLeaderLock(renewCtx, nodeID.String(), s.cfg.LeaderLockTTL)
		if err != nil || !ok {
			s.log.Warn("redis leader lock lost", "error", err)
			return false
		}
	} else if s.pgSession == nil || !s.pgSession.Alive(renewCtx) {
		s.log.Warn("postgres leader session lost")
		return false
	}
	guardCtx, guardCancel := context.WithTimeout(ctx, 2*time.Second)
	defer guardCancel()
	ok, err := s.st.RenewLeaderGuard(guardCtx, nodeID, s.cfg.LeaderGuardTTL)
	if err != nil || !ok {
		s.log.Warn("leader DB guard lost", "error", err)
		return false
	}
	return true
}

// resignLeadership stops cron/liveness immediately and releases the locks it
// owns. If the DB guard was lost to another owner, the guard release is a no-op.
func (s *Scheduler) resignLeadership(ctx context.Context, reason string) {
	if !s.leaderActive {
		return
	}
	nodeID, err := uuid.Parse(s.cfg.NodeID)
	releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err == nil {
		_ = s.st.ReleaseLeaderGuard(releaseCtx, nodeID)
	}
	if s.leaderMode == leaderRedis && err == nil {
		_ = s.presence.ReleaseLeaderLock(releaseCtx, nodeID.String())
	}
	if s.pgSession != nil {
		s.pgSession.Release(releaseCtx)
		s.pgSession = nil
	}
	s.log.Warn("leader released", "node", s.cfg.NodeID, "mode", s.leaderMode, "reason", reason)
	s.leaderActive = false
	s.leaderMode = ""
	_ = ctx
}

func (s *Scheduler) isLeader() bool { return s.leaderActive }
