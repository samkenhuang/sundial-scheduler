package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	// MaxDependencyGraphDepth is the deepest neighborhood the API renders. The
	// edit path allows dependency chains up to 100, but a single screen only
	// needs a bounded slice.
	MaxDependencyGraphDepth = 5
	// MaxDependencyGraphNodes bounds fan-in/fan-out explosions. The UI shows a
	// truncation notice instead of rendering an unusable graph.
	MaxDependencyGraphNodes = 200
)

// DependencyGraphRun is the most recent non-archived build of a graph node.
// Inputs is kept raw so the browser can show exactly what triggered the build.
type DependencyGraphRun struct {
	ID         uuid.UUID       `json:"id"`
	State      string          `json:"state"`
	LaunchMode string          `json:"launchMode"`
	TimeHour   string          `json:"timeHour"`
	Attempt    int             `json:"attempt"`
	CreatedAt  time.Time       `json:"createdAt"`
	StartedAt  *time.Time      `json:"startedAt,omitempty"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	Error      *string         `json:"error,omitempty"`
	Inputs     json.RawMessage `json:"inputs,omitempty"`
}

type DependencyGraphNode struct {
	ID         uuid.UUID           `json:"id"`
	Name       string              `json:"name"`
	ProjectID  *uuid.UUID          `json:"projectId,omitempty"`
	ArchivedAt *time.Time          `json:"archivedAt,omitempty"`
	Direction  string              `json:"direction"` // center | upstream | downstream
	Depth      int                 `json:"depth"`
	LatestRun  *DependencyGraphRun `json:"latestRun,omitempty"`
}

type DependencyGraphEdge struct {
	UpstreamJobID   uuid.UUID `json:"upstreamJobId"`
	DownstreamJobID uuid.UUID `json:"downstreamJobId"`
	Condition       string    `json:"condition"`
	Required        bool      `json:"required"`
}

type DependencyGraph struct {
	JobID     uuid.UUID             `json:"jobId"`
	Depth     int                   `json:"depth"`
	Nodes     []DependencyGraphNode `json:"nodes"`
	Edges     []DependencyGraphEdge `json:"edges"`
	Truncated bool                  `json:"truncated"`
}

type graphNodeRef struct {
	id        uuid.UUID
	direction string
	depth     int
}

// GetDependencyGraph returns the center job plus up to depth hops of upstream
// and downstream neighbors. It reads snapshots only; it never locks dependency
// rows or changes readiness state.
//
// Traversal is deliberately breadth-first in Go with a hard per-request node
// cap instead of one recursive CTE. A recursive CTE must materialize the whole
// reachable set before the outer LIMIT can stop it; with broad fan-out that can
// mean hundreds of thousands of rows and seconds of DB CPU for a page that will
// display at most 200 nodes. Each BFS step uses the dependency indexes and
// carries a LIMIT, so a pathological graph costs a bounded neighborhood query.
func (s *Store) GetDependencyGraph(ctx context.Context, jobID uuid.UUID, depth int) (DependencyGraph, error) {
	if depth < 1 {
		depth = 3
	}
	if depth > MaxDependencyGraphDepth {
		depth = MaxDependencyGraphDepth
	}
	nodes := map[uuid.UUID]*graphNodeRef{
		jobID: {id: jobID, direction: "center", depth: 0},
	}
	order := []uuid.UUID{jobID}
	edges := map[[2]uuid.UUID]DependencyGraphEdge{}
	truncated := false

	upFrontier := []uuid.UUID{jobID}
	downFrontier := []uuid.UUID{jobID}
	for level := 1; level <= depth; level++ {
		var err error
		upFrontier, err = s.expandDependencyFrontier(ctx, upFrontier, "upstream", level, nodes, &order, edges, &truncated)
		if err != nil {
			return DependencyGraph{}, err
		}
		downFrontier, err = s.expandDependencyFrontier(ctx, downFrontier, "downstream", level, nodes, &order, edges, &truncated)
		if err != nil {
			return DependencyGraph{}, err
		}
		if len(upFrontier) == 0 && len(downFrontier) == 0 {
			break
		}
	}

	graph := DependencyGraph{
		JobID: jobID, Depth: depth,
		Nodes: []DependencyGraphNode{}, Edges: []DependencyGraphEdge{},
		Truncated: truncated,
	}
	details, err := s.dependencyGraphDetails(ctx, order)
	if err != nil {
		return DependencyGraph{}, err
	}
	for _, id := range order {
		ref := nodes[id]
		detail, ok := details[id]
		if !ok {
			continue
		}
		detail.Direction = ref.direction
		detail.Depth = ref.depth
		graph.Nodes = append(graph.Nodes, detail)
	}
	sort.SliceStable(graph.Nodes, func(i, j int) bool {
		if graph.Nodes[i].Depth != graph.Nodes[j].Depth {
			return graph.Nodes[i].Depth < graph.Nodes[j].Depth
		}
		if graph.Nodes[i].Direction != graph.Nodes[j].Direction {
			return graph.Nodes[i].Direction < graph.Nodes[j].Direction
		}
		return graph.Nodes[i].Name < graph.Nodes[j].Name
	})
	for _, edge := range edges {
		if _, ok := nodes[edge.UpstreamJobID]; !ok {
			continue
		}
		if _, ok := nodes[edge.DownstreamJobID]; !ok {
			continue
		}
		graph.Edges = append(graph.Edges, edge)
	}
	sort.Slice(graph.Edges, func(i, j int) bool {
		if graph.Edges[i].DownstreamJobID != graph.Edges[j].DownstreamJobID {
			return graph.Edges[i].DownstreamJobID.String() < graph.Edges[j].DownstreamJobID.String()
		}
		return graph.Edges[i].UpstreamJobID.String() < graph.Edges[j].UpstreamJobID.String()
	})
	return graph, nil
}

// expandDependencyFrontier reads one BFS layer for one direction. The SQL LIMIT
// is remaining+1 so the caller can distinguish "exactly full" from "there are
// more neighbors than we will render" without traversing the rest of the graph.
func (s *Store) expandDependencyFrontier(ctx context.Context, frontier []uuid.UUID, direction string, level int,
	nodes map[uuid.UUID]*graphNodeRef, order *[]uuid.UUID,
	edges map[[2]uuid.UUID]DependencyGraphEdge, truncated *bool) ([]uuid.UUID, error) {
	if len(frontier) == 0 {
		return nil, nil
	}
	remaining := MaxDependencyGraphNodes - len(nodes)
	if remaining <= 0 {
		// We win the page-latency tradeoff by reporting truncation conservatively
		// at the cap instead of scanning the remaining frontier just to prove
		// whether it has another edge. A full 200-node graph is already at the
		// display limit, so the notice remains accurate enough ("达到或超过").
		*truncated = true
		return nil, nil
	}
	var rows []DependencyGraphEdge
	var err error
	if direction == "upstream" {
		rows, err = s.queryDependencyEdges(ctx, frontier, remaining+1, true)
	} else {
		rows, err = s.queryDependencyEdges(ctx, frontier, remaining+1, false)
	}
	if err != nil {
		return nil, err
	}
	next := make([]uuid.UUID, 0, len(rows))
	for _, edge := range rows {
		edges[[2]uuid.UUID{edge.UpstreamJobID, edge.DownstreamJobID}] = edge
		neighbor := edge.UpstreamJobID
		if direction == "downstream" {
			neighbor = edge.DownstreamJobID
		}
		if _, ok := nodes[neighbor]; ok {
			continue
		}
		if len(nodes) >= MaxDependencyGraphNodes {
			*truncated = true
			continue
		}
		nodes[neighbor] = &graphNodeRef{id: neighbor, direction: direction, depth: level}
		*order = append(*order, neighbor)
		next = append(next, neighbor)
	}
	if len(rows) > remaining {
		*truncated = true
	}
	return next, nil
}

func (s *Store) queryDependencyEdges(ctx context.Context, frontier []uuid.UUID, limit int, upstream bool) ([]DependencyGraphEdge, error) {
	var rows pgx.Rows
	var err error
	if upstream {
		rows, err = s.pool.Query(ctx, `
			SELECT d.upstream_job_id, d.downstream_job_id, d.condition, d.required
			FROM job_dependency d
			WHERE d.downstream_job_id = ANY($1::uuid[])
			ORDER BY d.downstream_job_id, d.upstream_job_id
			LIMIT $2`, frontier, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT d.upstream_job_id, d.downstream_job_id, d.condition, d.required
			FROM job_dependency d
			WHERE d.upstream_job_id = ANY($1::uuid[])
			ORDER BY d.upstream_job_id, d.downstream_job_id
			LIMIT $2`, frontier, limit)
	}
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	out := make([]DependencyGraphEdge, 0, limit)
	for rows.Next() {
		var edge DependencyGraphEdge
		if err := rows.Scan(&edge.UpstreamJobID, &edge.DownstreamJobID, &edge.Condition, &edge.Required); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, edge)
	}
	return out, wrapErr(rows.Err())
}

func (s *Store) dependencyGraphDetails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]DependencyGraphNode, error) {
	out := make(map[uuid.UUID]DependencyGraphNode, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, j.name, j.project_id, j.archived_at,
		       r.id, r.state, r.launch_mode, r.time_hour, r.attempt, r.created_at,
		       r.started_at, r.finished_at, r.error, r.inputs
		FROM job j
		LEFT JOIN LATERAL (
			SELECT id, state, launch_mode, time_hour, attempt, created_at,
			       started_at, finished_at, error, inputs
			FROM run
			WHERE job_id=j.id AND archived_at IS NULL
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) r ON true
		WHERE j.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var node DependencyGraphNode
		var projectID, latestID uuid.NullUUID
		var archived, started, finished sql.NullTime
		var state, launchMode, timeHour, errText sql.NullString
		var attempt sql.NullInt32
		var createdAt sql.NullTime
		var inputs []byte
		if err := rows.Scan(&node.ID, &node.Name, &projectID, &archived,
			&latestID, &state, &launchMode, &timeHour, &attempt, &createdAt,
			&started, &finished, &errText, &inputs); err != nil {
			return nil, wrapErr(err)
		}
		if projectID.Valid {
			v := projectID.UUID
			node.ProjectID = &v
		}
		if archived.Valid {
			v := archived.Time
			node.ArchivedAt = &v
		}
		if latestID.Valid {
			run := &DependencyGraphRun{
				ID: latestID.UUID, State: state.String, LaunchMode: launchMode.String,
				TimeHour: timeHour.String, Attempt: int(attempt.Int32), CreatedAt: createdAt.Time,
			}
			if started.Valid {
				v := started.Time
				run.StartedAt = &v
			}
			if finished.Valid {
				v := finished.Time
				run.FinishedAt = &v
			}
			if errText.Valid {
				v := errText.String
				run.Error = &v
			}
			if len(inputs) > 0 && string(inputs) != "null" {
				run.Inputs = json.RawMessage(inputs)
			}
			node.LatestRun = run
		}
		out[node.ID] = node
	}
	return out, wrapErr(rows.Err())
}
