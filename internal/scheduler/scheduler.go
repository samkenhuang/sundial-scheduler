package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"scheduler-platform/internal/config"
	"scheduler-platform/internal/presence"
	"scheduler-platform/internal/store"
)

type Scheduler struct {
	cfg      config.Config
	st       *store.Store
	log      *slog.Logger
	presence *presence.Fallback
	liveness *livenessSource

	lastLiveness time.Time

	leaderActive bool
	leaderMode   leaderMode
	leaderSince  time.Time
	pgSession    *store.LeaderSession
}

func New(cfg config.Config, st *store.Store, log *slog.Logger) *Scheduler {
	pres := presence.New(cfg.RedisURL, cfg.PresenceTTL, log)
	return &Scheduler{
		cfg: cfg, st: st, log: log, presence: pres,
		liveness: newLivenessSource(pres, cfg.PresenceTTL, cfg.LivenessGrace, cfg.RedisWarmup, log),
	}
}

func (s *Scheduler) Run(ctx context.Context) error {
	defer s.presence.Close()
	nodeID, err := uuid.Parse(s.cfg.NodeID)
	if err != nil {
		return fmt.Errorf("invalid SCHED_NODE_ID: %w", err)
	}
	if err := s.st.RegisterSchedNode(ctx, nodeID, s.cfg.NodeAddr); err != nil {
		return err
	}
	s.log.Info("scheduler started", "node", nodeID, "addr", s.cfg.NodeAddr)

	heartbeat := time.NewTicker(s.cfg.Heartbeat)
	defer heartbeat.Stop()
	work := time.NewTicker(time.Second)
	defer work.Stop()

	for {
		select {
		case <-ctx.Done():
			s.shutdown(nodeID)
			return nil
		case <-heartbeat.C:
			_ = s.st.HeartbeatSchedNode(ctx, nodeID)
		case <-work.C:
			if !s.syncLeadership(ctx, nodeID) {
				continue
			}
			_, _ = s.st.ExpireCommands(ctx, 1000)
			s.doWork(ctx)
			s.fireSchedules(ctx)
		}
	}
}

func (s *Scheduler) doWork(ctx context.Context) {
	if !s.isLeader() {
		return
	}
	// A leader gets a grace window after election before presence expiry is
	// interpreted as worker loss. Workers keep executing during the outage.
	if time.Since(s.leaderSince) < s.cfg.RecoveryGrace {
		return
	}
	if time.Since(s.lastLiveness) < s.cfg.LivenessPoll {
		return
	}
	s.lastLiveness = time.Now()
	// Liveness is Redis-first. DB only supplies the owner snapshot and the
	// Redis-failure fallback heartbeat; it must not be queried per owner.
	owners, err := s.st.RunningOwners(ctx, 5000, s.cfg.PresenceTTL)
	if err != nil {
		s.log.Error("list running owners", "error", err)
		return
	}
	for _, owner := range owners {
		alive, source := s.liveness.Alive(ctx, owner.WorkerID, owner.DBFresh)
		if alive {
			continue
		}
		n, err := s.st.FailWorkerRuns(ctx, owner.WorkerID, 100)
		if err != nil {
			s.log.Error("fail offline worker runs", "worker_id", owner.WorkerID, "error", err)
			continue
		}
		if err := s.st.MarkWorkerOffline(ctx, owner.WorkerID); err != nil {
			s.log.Error("mark worker offline", "worker_id", owner.WorkerID, "error", err)
		}
		if n > 0 {
			s.log.Warn("failed runs from offline worker", "worker_id", owner.WorkerID, "runs", n, "source", source)
		}
	}
}

func (s *Scheduler) fireSchedules(ctx context.Context) {
	if !s.isLeader() {
		return
	}
	jobs, err := s.st.ListDueJobs(ctx, 1000)
	if err != nil {
		s.log.Error("list due jobs", "error", err)
		return
	}
	for _, job := range jobs {
		fired, next, err := s.st.FireDueJobCatchUp(ctx, job.JobID, job.NextFire, s.cfg.CronCatchUpLimit)
		if err != nil {
			s.log.Error("fire job", "job", job.JobID, "error", err)
			continue
		}
		if fired > 0 {
			s.log.Info("cron caught up", "job", job.JobID, "runs", fired, "next", next)
		}
	}
}

func (s *Scheduler) shutdown(nodeID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.resignLeadership(ctx, "shutdown")
	_ = s.st.SetSchedNodeDraining(ctx, nodeID, true)
}
