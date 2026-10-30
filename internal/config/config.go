package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Config is intentionally environment-driven: the P0 deployment model is
// systemd/VM first, with Docker Compose only for local development.
type Config struct {
	DatabaseURL string

	APIAddr    string
	WorkerAddr string

	NodeID   string
	NodeAddr string

	RedisURL       string
	PresenceTTL    time.Duration
	LeaderLockTTL  time.Duration
	LeaderGuardTTL time.Duration

	DevToken    string
	WorkerToken string

	WorkerLabels      []string
	WorkerConcurrency int
	GatewayURL        string
	WorkerID          string

	LogRoot          string
	SpoolRoot        string
	RunWorkspaceRoot string

	ClaimWait            time.Duration
	CommandWait          time.Duration
	Heartbeat            time.Duration
	LivenessPoll         time.Duration
	LivenessGrace        time.Duration
	RedisWarmup          time.Duration
	RecoveryGrace        time.Duration
	LogChunkBytes        int64
	LogChunkEvery        time.Duration
	LogQuotaBytes        int64
	LogHighWater         float64
	LogLowWater          float64
	LogRetention         time.Duration
	LogArchiveInterval   time.Duration
	LogArchiveBatch      int
	LogStreamCallbackURL string

	CronCatchUpLimit int
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL:          env("SCHED_DATABASE_URL", "postgres://sched:sched@localhost:55432/sched?sslmode=disable"),
		APIAddr:              env("SCHED_API_ADDR", ":8080"),
		WorkerAddr:           env("SCHED_WORKER_ADDR", ":8081"),
		NodeID:               env("SCHED_NODE_ID", ""),
		NodeAddr:             env("SCHED_NODE_ADDR", "http://127.0.0.1:8080"),
		RedisURL:             env("SCHED_REDIS_URL", ""),
		PresenceTTL:          envDuration("SCHED_PRESENCE_TTL", 60*time.Second),
		LeaderLockTTL:        envDuration("SCHED_LEADER_LOCK_TTL", 10*time.Second),
		LeaderGuardTTL:       envDuration("SCHED_LEADER_GUARD_TTL", 15*time.Second),
		DevToken:             env("SCHED_DEV_TOKEN", "dev-token"),
		WorkerToken:          env("SCHED_WORKER_TOKEN", "dev-worker-token"),
		GatewayURL:           env("SCHED_GATEWAY_URL", "http://127.0.0.1:8081"),
		WorkerID:             env("SCHED_WORKER_ID", ""),
		LogRoot:              env("SCHED_LOG_ROOT", "./var/logs"),
		SpoolRoot:            env("SCHED_SPOOL_ROOT", "./var/spool"),
		RunWorkspaceRoot:     env("SCHED_WORKSPACE_ROOT", "./var/workspaces"),
		ClaimWait:            envDuration("SCHED_CLAIM_WAIT", 30*time.Second),
		CommandWait:          envDuration("SCHED_COMMAND_WAIT", 30*time.Second),
		Heartbeat:            envDuration("SCHED_HEARTBEAT_INTERVAL", 20*time.Second),
		LivenessPoll:         envDuration("SCHED_LIVENESS_POLL", 5*time.Second),
		LivenessGrace:        envDuration("SCHED_LIVENESS_GRACE", 90*time.Second),
		RedisWarmup:          envDuration("SCHED_REDIS_WARMUP", 60*time.Second),
		RecoveryGrace:        envDuration("SCHED_RECOVERY_GRACE", 60*time.Second),
		LogChunkBytes:        envInt64("SCHED_LOG_CHUNK_BYTES", 4<<20),
		LogChunkEvery:        envDuration("SCHED_LOG_CHUNK_INTERVAL", 5*time.Second),
		LogQuotaBytes:        envInt64("SCHED_LOG_QUOTA_BYTES", 200<<30),
		LogHighWater:         envFloat("SCHED_LOG_HIGH_WATER", 0.85),
		LogLowWater:          envFloat("SCHED_LOG_LOW_WATER", 0.70),
		LogRetention:         envDuration("SCHED_LOG_RETENTION", 14*24*time.Hour),
		LogArchiveInterval:   envDuration("SCHED_LOG_ARCHIVE_INTERVAL", time.Hour),
		LogArchiveBatch:      envInt("SCHED_LOG_ARCHIVE_BATCH", 500),
		LogStreamCallbackURL: strings.TrimRight(os.Getenv("SCHED_LOG_STREAM_CALLBACK_URL"), "/"),
		CronCatchUpLimit:     envInt("SCHED_CRON_CATCHUP_LIMIT", 100),
	}
	if c.NodeID == "" {
		c.NodeID = uuid.NewString()
	}
	if c.WorkerID == "" {
		c.WorkerID = uuid.NewString()
	}
	if labels := strings.TrimSpace(os.Getenv("SCHED_WORKER_LABELS")); labels != "" {
		for _, l := range strings.Split(labels, ",") {
			if l = strings.TrimSpace(l); l != "" {
				c.WorkerLabels = append(c.WorkerLabels, l)
			}
		}
	}
	c.WorkerConcurrency = envInt("SCHED_WORKER_CONCURRENCY", 16)
	if c.WorkerConcurrency < 1 {
		return c, fmt.Errorf("SCHED_WORKER_CONCURRENCY must be >= 1")
	}
	if c.LogArchiveBatch < 1 {
		c.LogArchiveBatch = 500
	}
	if c.LogArchiveBatch > 5000 {
		c.LogArchiveBatch = 5000
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("SCHED_DATABASE_URL is required")
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(env(key, ""))
	if err != nil {
		return fallback
	}
	return v
}

func envInt64(key string, fallback int64) int64 {
	v, err := strconv.ParseInt(env(key, ""), 10, 64)
	if err != nil {
		return fallback
	}
	return v
}

func envFloat(key string, fallback float64) float64 {
	v, err := strconv.ParseFloat(env(key, ""), 64)
	if err != nil {
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(env(key, ""))
	if err != nil {
		return fallback
	}
	return v
}
