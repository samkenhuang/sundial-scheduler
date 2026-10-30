package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"scheduler-platform/internal/api"
	"scheduler-platform/internal/config"
	"scheduler-platform/internal/db"
	"scheduler-platform/internal/scheduler"
	"scheduler-platform/internal/store"
	"scheduler-platform/internal/worker"
)

const usage = `usage: sched <server|scheduler|worker|migrate>

  server     API and Worker Gateway (same binary/domain behind one ingress)
  scheduler  cron dispatcher, global liveness reaper
  worker     outbound-only execution agent
  migrate    apply schema migrations
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "migrate":
		dir := os.Getenv("SCHED_MIGRATIONS_DIR")
		if dir == "" {
			dir = "migrations"
		}
		if err := db.Migrate(ctx, cfg.DatabaseURL, dir); err != nil {
			log.Error("migration failed", "error", err)
			os.Exit(1)
		}
		log.Info("migrations applied")
	case "server":
		pool, err := db.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			log.Error("database", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
		if err := api.New(cfg, store.New(pool), log).Run(ctx); err != nil {
			log.Error("server stopped", "error", err)
			os.Exit(1)
		}
	case "scheduler":
		pool, err := db.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			log.Error("database", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
		if err := scheduler.New(cfg, store.New(pool), log).Run(ctx); err != nil {
			log.Error("scheduler stopped", "error", err)
			os.Exit(1)
		}
	case "worker":
		// Workers deliberately do not open a database connection. Every
		// control-plane interaction goes through the Gateway over outbound HTTPS.
		if err := worker.New(cfg, log).Run(ctx); err != nil {
			log.Error("worker stopped", "error", err)
			os.Exit(1)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}
