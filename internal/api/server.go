package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"scheduler-platform/internal/config"
	"scheduler-platform/internal/presence"
	"scheduler-platform/internal/store"
	"scheduler-platform/internal/web"
)

type Server struct {
	cfg        config.Config
	store      *store.Store
	presence   presence.Store
	log        *slog.Logger
	logStreams *logStreamHub
	startedAt  time.Time
}

func New(cfg config.Config, st *store.Store, log *slog.Logger) *Server {
	return &Server{
		cfg: cfg, store: st, presence: presence.New(cfg.RedisURL, cfg.PresenceTTL, log),
		log: log, logStreams: newLogStreamHub(), startedAt: time.Now().UTC(),
	}
}

func (s *Server) Run(ctx context.Context) error {
	defer s.presence.Close()
	go s.logArchiveLoop(ctx)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /healthz", s.handleHealth)
	apiMux.HandleFunc("POST /v1/projects", s.handleCreateProject)
	apiMux.HandleFunc("GET /v1/projects", s.handleListProjects)
	apiMux.HandleFunc("POST /v1/jobs", s.handleCreateJob)
	apiMux.HandleFunc("GET /v1/jobs", s.handleListJobs)
	apiMux.HandleFunc("GET /v1/jobs/overview", s.handleListJobsOverview)
	apiMux.HandleFunc("GET /v1/jobs/{id}", s.handleGetJob)
	apiMux.HandleFunc("PATCH /v1/jobs/{id}", s.handleRenameJob)
	apiMux.HandleFunc("DELETE /v1/jobs/{id}", s.handleArchiveJob)
	apiMux.HandleFunc("POST /v1/jobs/{id}/versions", s.handlePublishVersion)
	apiMux.HandleFunc("PATCH /v1/jobs/{id}/schedule", s.handleUpdateSchedule)
	apiMux.HandleFunc("PATCH /v1/jobs/{id}/log-retention", s.handleUpdateLogRetention)
	apiMux.HandleFunc("GET /v1/jobs/{id}/dependencies", s.handleGetJobDependencies)
	apiMux.HandleFunc("GET /v1/jobs/{id}/dependency-graph", s.handleGetDependencyGraph)
	apiMux.HandleFunc("PUT /v1/jobs/{id}/dependencies", s.handleSetJobDependencies)
	apiMux.HandleFunc("POST /v1/jobs/{id}/runs", s.handleTriggerRun)
	apiMux.HandleFunc("GET /v1/runs", s.handleListRuns)
	apiMux.HandleFunc("GET /v1/runs/{id}", s.handleGetRun)
	apiMux.HandleFunc("POST /v1/runs/{id}/cancel", s.handleCancelRun)
	apiMux.HandleFunc("POST /v1/runs/{id}/log-ticket", s.handleLogTicket)
	apiMux.HandleFunc("GET /v1/runs/{id}/log", s.handleGetLog)
	apiMux.HandleFunc("GET /v1/runs/{id}/log/stream", s.handleStreamLog)
	apiMux.HandleFunc("GET /v1/workers", s.handleListWorkers)
	apiMux.HandleFunc("POST /v1/workers", s.handleCreateWorker)
	apiMux.HandleFunc("GET /v1/workers/{id}", s.handleGetWorker)
	apiMux.HandleFunc("PATCH /v1/workers/{id}", s.handleUpdateWorker)
	apiMux.HandleFunc("DELETE /v1/workers/{id}", s.handleDeleteWorker)
	apiMux.HandleFunc("POST /v1/admin/workers/{id}/drain", s.handleDrainWorker)
	apiMux.HandleFunc("GET /v1/admin/control-plane", s.handleControlPlane)
	apiMux.HandleFunc("GET /v1/admin/worker-access", s.handleWorkerAccess)
	apiMux.HandleFunc("GET /v1/admin/worker-binary", s.handleWorkerBinaryDownload)
	apiMux.HandleFunc("POST /v1/admin/logs/cleanup", s.handleAdminLogCleanup)
	apiMux.HandleFunc("POST /v1/admin/logs/archive", s.handleAdminLogArchive)

	workerMux := http.NewServeMux()
	workerMux.HandleFunc("POST /worker/v1/register", s.handleWorkerRegister)
	workerMux.HandleFunc("POST /worker/v1/claim", s.handleWorkerClaim)
	workerMux.HandleFunc("GET /worker/v1/commands", s.handleWorkerCommands)
	workerMux.HandleFunc("POST /worker/v1/heartbeat", s.handleWorkerHeartbeat)
	// Kept as a design-compatible alias for per-run heartbeat clients.
	workerMux.HandleFunc("POST /worker/v1/runs/{id}/heartbeat", s.handleWorkerHeartbeat)
	workerMux.HandleFunc("POST /worker/v1/runs/{id}/complete", s.handleWorkerComplete)
	workerMux.HandleFunc("POST /worker/v1/runs/{id}/log", s.handleWorkerUploadLog)
	workerMux.HandleFunc("POST /worker/v1/commands/{id}/result", s.handleWorkerCommandResult)
	workerMux.HandleFunc("POST /worker/v1/log-streams/{id}", s.handleWorkerLogStream)
	workerMux.HandleFunc("POST /worker/v1/log-streams/{id}/result", s.handleWorkerLogStreamResult)

	apiHandler := s.devAuth(apiMux)
	rootMux := http.NewServeMux()
	rootMux.Handle("/ui/", http.StripPrefix("/ui/", web.Handler()))
	rootMux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})
	rootMux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui/", http.StatusFound)
			return
		}
		apiHandler.ServeHTTP(w, r)
	}))

	apiSrv := &http.Server{
		Addr:              s.cfg.APIAddr,
		Handler:           s.logRequests(rootMux),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	workerSrv := &http.Server{
		Addr:              s.cfg.WorkerAddr,
		Handler:           s.logRequests(s.workerAuth(workerMux)),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		s.log.Info("api listening", "addr", s.cfg.APIAddr)
		errCh <- apiSrv.ListenAndServe()
	}()
	go func() {
		s.log.Info("worker gateway listening", "addr", s.cfg.WorkerAddr)
		errCh <- workerSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = apiSrv.Shutdown(shutdownCtx)
	_ = workerSrv.Shutdown(shutdownCtx)
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Pool().Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db_unavailable", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("http request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}
