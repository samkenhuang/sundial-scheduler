package api

import (
	"context"
	"net/http"
	"runtime"
	"time"
)

const (
	controlPlaneHealthy   = "HEALTHY"
	controlPlaneDegraded  = "DEGRADED"
	controlPlaneUnhealthy = "UNHEALTHY"
	controlPlaneUnknown   = "UNKNOWN"
)

type dependencyStatusView struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latencyMs,omitempty"`
}

type controlPlaneServerView struct {
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"startedAt"`
	UptimeSeconds int64     `json:"uptimeSeconds"`
	APIAddr       string    `json:"apiAddr"`
	WorkerAddr    string    `json:"workerAddr"`
	GatewayURL    string    `json:"gatewayUrl"`
	GoVersion     string    `json:"goVersion"`
	GOOS          string    `json:"goos"`
	GOARCH        string    `json:"goarch"`
}

type controlPlaneLeaderView struct {
	NodeID    string     `json:"nodeId,omitempty"`
	Mode      string     `json:"mode,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Active    bool       `json:"active"`
}

type controlPlaneNodeView struct {
	ID                  string    `json:"id"`
	Addr                string    `json:"addr"`
	State               string    `json:"state"`
	Status              string    `json:"status"`
	Leader              bool      `json:"leader"`
	Stale               bool      `json:"stale"`
	StartedAt           time.Time `json:"startedAt"`
	LastHeartbeatAt     time.Time `json:"lastHeartbeatAt"`
	HeartbeatAgeSeconds int64     `json:"heartbeatAgeSeconds"`
}

type controlPlaneSchedulerView struct {
	Status     string                 `json:"status"`
	NodeCount  int                    `json:"nodeCount"`
	AliveCount int                    `json:"aliveCount"`
	Leader     controlPlaneLeaderView `json:"leader"`
	Nodes      []controlPlaneNodeView `json:"nodes"`
}

type controlPlaneView struct {
	CheckedAt time.Time                 `json:"checkedAt"`
	Status    string                    `json:"status"`
	Server    controlPlaneServerView    `json:"server"`
	Database  dependencyStatusView      `json:"database"`
	Redis     dependencyStatusView      `json:"redis"`
	Scheduler controlPlaneSchedulerView `json:"scheduler"`
}

func (s *Server) handleControlPlane(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	now := time.Now().UTC()
	startedAt := s.startedAt
	if startedAt.IsZero() {
		startedAt = now
	}
	view := controlPlaneView{
		CheckedAt: now,
		Server: controlPlaneServerView{
			Status:        "ONLINE",
			StartedAt:     startedAt,
			UptimeSeconds: int64(now.Sub(startedAt).Seconds()),
			APIAddr:       s.cfg.APIAddr,
			WorkerAddr:    s.cfg.WorkerAddr,
			GatewayURL:    s.cfg.GatewayURL,
			GoVersion:     runtime.Version(),
			GOOS:          runtime.GOOS,
			GOARCH:        runtime.GOARCH,
		},
		Scheduler: controlPlaneSchedulerView{
			Status: controlPlaneUnknown,
			Nodes:  []controlPlaneNodeView{},
		},
	}

	dbStart := time.Now()
	view.Database = dependencyStatusView{Status: "UNAVAILABLE", LatencyMS: 0}
	dbErr := s.store.Pool().Ping(ctx)
	view.Database.LatencyMS = time.Since(dbStart).Milliseconds()
	if dbErr == nil {
		view.Database.Status = "ONLINE"
	}

	switch {
	case s.cfg.RedisURL == "":
		view.Redis = dependencyStatusView{Status: "NOT_CONFIGURED"}
	case s.presence.Available():
		view.Redis = dependencyStatusView{Status: "ONLINE"}
	default:
		view.Redis = dependencyStatusView{Status: "UNAVAILABLE"}
	}

	leaderActive := false
	leaderFresh := false
	if dbErr == nil {
		guard, guardErr := s.store.GetLeaderGuard(ctx)
		if guardErr == nil {
			leaderActive = guard.OwnerID != "" && guard.ExpiresAt.After(now)
			view.Scheduler.Leader = controlPlaneLeaderView{
				NodeID: guard.OwnerID,
				Mode:   guard.Mode,
				Active: leaderActive,
			}
			if leaderActive {
				expiresAt := guard.ExpiresAt
				view.Scheduler.Leader.ExpiresAt = &expiresAt
			}
		}

		staleAfter := 3 * s.cfg.Heartbeat
		if staleAfter < 30*time.Second {
			staleAfter = 30 * time.Second
		}
		nodes, nodesErr := s.store.ListSchedNodes(ctx, now.Add(-24*time.Hour), 100)
		if nodesErr == nil {
			for _, node := range nodes {
				age := now.Sub(node.HeartbeatAt)
				if age < 0 {
					age = 0
				}
				leader := leaderActive && guard.OwnerID == node.ID.String()
				stale := node.HeartbeatAt.Before(now.Add(-staleAfter))
				if leader && !stale {
					leaderFresh = true
				}
				status := "FOLLOWER"
				switch {
				case leader:
					status = "LEADER"
				case node.State == "draining":
					status = "DRAINING"
				case stale:
					status = "STALE"
				}
				if !stale {
					view.Scheduler.AliveCount++
				}
				view.Scheduler.Nodes = append(view.Scheduler.Nodes, controlPlaneNodeView{
					ID:                  node.ID.String(),
					Addr:                node.Addr,
					State:               node.State,
					Status:              status,
					Leader:              leader,
					Stale:               stale,
					StartedAt:           node.StartedAt,
					LastHeartbeatAt:     node.HeartbeatAt,
					HeartbeatAgeSeconds: int64(age.Seconds()),
				})
			}
			view.Scheduler.NodeCount = len(view.Scheduler.Nodes)
			if leaderActive && leaderFresh {
				view.Scheduler.Status = controlPlaneHealthy
			} else {
				view.Scheduler.Status = controlPlaneDegraded
			}
		}
	}

	switch {
	case dbErr != nil:
		view.Status = controlPlaneUnhealthy
	case view.Scheduler.Status != controlPlaneHealthy:
		view.Status = controlPlaneDegraded
	case view.Redis.Status == "UNAVAILABLE":
		view.Status = controlPlaneDegraded
	default:
		view.Status = controlPlaneHealthy
	}

	writeJSON(w, http.StatusOK, view)
}
