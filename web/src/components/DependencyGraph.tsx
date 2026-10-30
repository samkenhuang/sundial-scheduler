import { useEffect, useId, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import type { DependencyGraph, DependencyGraphEdge, DependencyGraphNode } from "../api/types";
import { conditionLabel, formatDate, launchModeLabel, prettyJson, shortId, stateLabel } from "../lib/format";
import { StatusInline } from "./StatusBall";

const CARD_WIDTH = 208;
const CARD_HEIGHT = 104;
const COLUMN_WIDTH = 236;
const ROW_STEP = 118;
const HEADER_HEIGHT = 38;
const PAD_X = 16;
const PAD_Y = 12;
// A 199-child fan-out is common in real dependency graphs. Rendering every
// sibling as a 104px card produces a 23,000px column; preview the healthiest
// signal (problem states first), then expose the full layer on demand.
const MAX_VISIBLE_PER_COLUMN = 8;
const STATUS_PRIORITY: Record<string, number> = {
  FAILED: 0,
  TIMED_OUT: 1,
  CANCELED: 2,
  RUNNING: 3,
  QUEUED: 4,
  PENDING: 5,
  SUCCEEDED: 6,
};

type PositionedNode = DependencyGraphNode & {
  x: number;
  y: number;
  width: number;
  height: number;
};

type PositionedGroup = {
  columnKey: number;
  label: string;
  x: number;
  y: number;
  width: number;
  height: number;
  hidden: number;
  counts: Record<string, number>;
};

type GraphColumn = {
  key: number;
  label: string;
  x: number;
  count: number;
  center: boolean;
};

type GraphLayout = {
  width: number;
  height: number;
  nodes: PositionedNode[];
  groups: PositionedGroup[];
  columns: GraphColumn[];
  allByColumn: Map<number, DependencyGraphNode[]>;
  byId: Map<string, PositionedNode>;
};

function directionLabel(node: DependencyGraphNode): string {
  if (node.direction === "center") return "当前任务";
  return `${node.direction === "upstream" ? "上游" : "下游"}第 ${node.depth} 层`;
}

function inputsPreview(inputs: unknown): string | null {
  if (inputs === null || inputs === undefined) return null;
  try {
    return JSON.stringify(inputs);
  } catch {
    return String(inputs);
  }
}

function statusClass(node: DependencyGraphNode): string {
  return `dep-status-${node.latestRun?.state ?? "NOTBUILT"}`;
}

function average(values: number[]): number {
  if (!values.length) return Number.POSITIVE_INFINITY;
  return values.reduce((total, value) => total + value, 0) / values.length;
}

function countByState(nodes: DependencyGraphNode[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const node of nodes) {
    const state = node.latestRun?.state ?? "NOTBUILT";
    counts[state] = (counts[state] ?? 0) + 1;
  }
  return counts;
}

function stateSummary(counts: Record<string, number>): string {
  return Object.entries(counts)
    .sort(([a], [b]) => (STATUS_PRIORITY[a] ?? 9) - (STATUS_PRIORITY[b] ?? 9))
    .slice(0, 3)
    .map(([state, count]) => `${stateLabel(state)} ${count}`)
    .join(" · ");
}

function previewForColumn(nodes: DependencyGraphNode[], rows: Map<string, number>): DependencyGraphNode[] {
  const limit = nodes.length > 20 ? 4 : nodes.length > 8 ? 6 : MAX_VISIBLE_PER_COLUMN;
  if (nodes.length <= limit) {
    return [...nodes].sort((a, b) => (rows.get(a.id) ?? 0) - (rows.get(b.id) ?? 0));
  }
  const prioritized = [...nodes].sort((a, b) => {
    const aPriority = STATUS_PRIORITY[a.latestRun?.state ?? "NOTBUILT"] ?? 9;
    const bPriority = STATUS_PRIORITY[b.latestRun?.state ?? "NOTBUILT"] ?? 9;
    if (aPriority !== bPriority) return aPriority - bPriority;
    const rowDiff = (rows.get(a.id) ?? 0) - (rows.get(b.id) ?? 0);
    return rowDiff !== 0 ? rowDiff : a.name.localeCompare(b.name);
  });
  return prioritized
    .slice(0, limit)
    .sort((a, b) => (rows.get(a.id) ?? 0) - (rows.get(b.id) ?? 0));
}

function buildLayout(graph: DependencyGraph): GraphLayout {
  const columns = new Map<number, DependencyGraphNode[]>();
  for (const node of graph.nodes) {
    const key = node.direction === "upstream" ? -node.depth : node.direction === "downstream" ? node.depth : 0;
    const list = columns.get(key) ?? [];
    list.push(node);
    columns.set(key, list);
  }
  const keysWithNodes = [...columns.keys()].filter((key) => (columns.get(key)?.length ?? 0) > 0).sort((a, b) => a - b);
  const edges = graph.edges ?? [];
  const downstream = new Map<string, string[]>();
  const upstream = new Map<string, string[]>();
  for (const edge of edges) {
    const down = downstream.get(edge.upstreamJobId) ?? [];
    down.push(edge.downstreamJobId);
    downstream.set(edge.upstreamJobId, down);
    const up = upstream.get(edge.downstreamJobId) ?? [];
    up.push(edge.upstreamJobId);
    upstream.set(edge.downstreamJobId, up);
  }

  // Assign a row to every node. The center is fixed, then columns are visited
  // from the center outward and sorted by the barycenter of already-positioned
  // neighbors. This keeps fan-in/fan-out edges from crossing as much as a plain
  // alphabetical sort.
  const rows = new Map<string, number>();
  const centerNodes = [...(columns.get(0) ?? [])].sort((a, b) => a.name.localeCompare(b.name));
  centerNodes.forEach((node, index) => rows.set(node.id, index));
  const assignColumn = (nodes: DependencyGraphNode[], neighborIds: (node: DependencyGraphNode) => string[]) => {
    const sorted = [...nodes].sort((a, b) => {
      const aRows = neighborIds(a).map((id) => rows.get(id)).filter((row): row is number => row !== undefined);
      const bRows = neighborIds(b).map((id) => rows.get(id)).filter((row): row is number => row !== undefined);
      const diff = average(aRows) - average(bRows);
      if (Number.isFinite(diff) && diff !== 0) return diff;
      return a.name.localeCompare(b.name);
    });
    sorted.forEach((node, index) => rows.set(node.id, index));
  };
  for (const key of keysWithNodes.filter((key) => key < 0).sort((a, b) => b - a)) {
    assignColumn(columns.get(key) ?? [], (node) => downstream.get(node.id) ?? []);
  }
  for (const key of keysWithNodes.filter((key) => key > 0).sort((a, b) => a - b)) {
    assignColumn(columns.get(key) ?? [], (node) => upstream.get(node.id) ?? []);
  }

  const previews = new Map<number, DependencyGraphNode[]>();
  for (const key of keysWithNodes) {
    previews.set(key, previewForColumn(columns.get(key) ?? [], rows));
  }
  const maxRows = Math.max(
    1,
    ...keysWithNodes.map((key) => (previews.get(key)?.length ?? 0) + (columns.get(key)!.length > (previews.get(key)?.length ?? 0) ? 1 : 0)),
  );
  const width = PAD_X * 2 + keysWithNodes.length * COLUMN_WIDTH;
  const height = HEADER_HEIGHT + PAD_Y * 2 + maxRows * ROW_STEP;
  const graphColumns: GraphColumn[] = [];
  const groups: PositionedGroup[] = [];
  const positioned: PositionedNode[] = [];
  const allByColumn = new Map<number, DependencyGraphNode[]>();
  keysWithNodes.forEach((key, columnIndex) => {
    const allNodes = columns.get(key) ?? [];
    const preview = previews.get(key) ?? [];
    const hidden = allNodes.length - preview.length;
    const label = key === 0 ? "当前任务" : `${key < 0 ? "上游" : "下游"} ${Math.abs(key)} 层`;
    const columnX = PAD_X + columnIndex * COLUMN_WIDTH;
    allByColumn.set(key, allNodes);
    graphColumns.push({ key, label, x: columnX, count: allNodes.length, center: key === 0 });
    const visibleRows = preview.length + (hidden > 0 ? 1 : 0);
    const startY = HEADER_HEIGHT + PAD_Y + (maxRows * ROW_STEP - visibleRows * ROW_STEP) / 2;
    preview.forEach((node, rowIndex) => {
      positioned.push({
        ...node,
        x: columnX + (COLUMN_WIDTH - CARD_WIDTH) / 2,
        y: startY + rowIndex * ROW_STEP,
        width: CARD_WIDTH,
        height: CARD_HEIGHT,
      });
    });
    if (hidden > 0) {
      const hiddenNodes = allNodes.filter((node) => !preview.some((visible) => visible.id === node.id));
      groups.push({
        columnKey: key,
        label,
        x: columnX + (COLUMN_WIDTH - CARD_WIDTH) / 2,
        y: startY + preview.length * ROW_STEP,
        width: CARD_WIDTH,
        height: CARD_HEIGHT,
        hidden,
        counts: countByState(hiddenNodes),
      });
    }
  });
  const byId = new Map(positioned.map((node) => [node.id, node]));
  return { width, height, nodes: positioned, groups, columns: graphColumns, allByColumn, byId };
}

function edgePath(source: PositionedNode, target: PositionedNode): string {
  const startX = source.x + source.width;
  const startY = source.y + source.height / 2;
  const endX = target.x;
  const endY = target.y + target.height / 2;
  if (endX >= startX) {
    const curve = Math.max(28, (endX - startX) * 0.45);
    return `M ${startX} ${startY} C ${startX + curve} ${startY}, ${endX - curve} ${endY}, ${endX} ${endY}`;
  }
  // A back edge (shortcut between non-adjacent columns) gets a visible detour
  // instead of collapsing into a straight line behind the cards.
  const endXBack = target.x + target.width;
  return `M ${source.x} ${startY} C ${source.x - 46} ${startY}, ${endXBack + 46} ${endY}, ${endXBack} ${endY}`;
}

function NodeDetail({ node, onClose }: { node: DependencyGraphNode; onClose: () => void }) {
  const run = node.latestRun;
  const inputs = inputsPreview(run?.inputs);
  return (
    <section className="dep-node-detail" aria-label="选中节点详情">
      <div className="dep-node-detail-head">
        <div>
          <h3>{node.name}</h3>
          <p className="dep-node-detail-sub">
            {directionLabel(node)}
            {node.archivedAt ? " · 已归档" : ""}
          </p>
        </div>
        <button className="btn small" type="button" onClick={onClose}>
          关闭
        </button>
      </div>
      <div className="dep-node-detail-grid">
        <div>
          <span className="dep-detail-label">状态</span>
          <StatusInline state={run?.state} />
        </div>
        <div>
          <span className="dep-detail-label">最近构建</span>
          {run ? (
            <Link className="mono" to={`/run/${run.id}`}>
              #{shortId(run.id)}
            </Link>
          ) : (
            "—"
          )}
        </div>
        <div>
          <span className="dep-detail-label">time_hour</span>
          <span>{run?.timeHour ?? "—"}</span>
        </div>
        <div>
          <span className="dep-detail-label">触发方式</span>
          <span>{run ? launchModeLabel(run.launchMode) : "—"}</span>
        </div>
        <div>
          <span className="dep-detail-label">运行时间</span>
          <span>{run ? formatDate(run.createdAt) : "—"}</span>
        </div>
        <div>
          <span className="dep-detail-label">任务链接</span>
          <Link to={`/job/${node.id}`}>打开任务</Link>
        </div>
      </div>
      <div className="dep-node-detail-params">
        <span className="dep-detail-label">最近一次参数</span>
        {inputs ? <pre>{prettyJson(run?.inputs)}</pre> : <p className="muted">—</p>}
      </div>
      {run?.error ? (
        <p className="dep-node-detail-error" role="alert">
          错误：{run.error}
        </p>
      ) : null}
    </section>
  );
}

function LayerSheet({
  column,
  nodes,
  selectedId,
  onSelect,
  onClose,
}: {
  column: GraphColumn;
  nodes: DependencyGraphNode[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  onClose: () => void;
}) {
  const [filter, setFilter] = useState("");
  const normalized = filter.trim().toLowerCase();
  const filtered = normalized ? nodes.filter((node) => node.name.toLowerCase().includes(normalized)) : nodes;
  return (
    <section className="dep-layer-sheet" aria-label={`${column.label}全部任务`}>
      <div className="dep-layer-sheet-head">
        <div>
          <h3>{column.label}</h3>
          <p>
            {filtered.length === nodes.length ? `${nodes.length} 个任务` : `${filtered.length} / ${nodes.length} 个任务`}
            ，点击一行查看完整最近参数
          </p>
        </div>
        <button className="btn small" type="button" onClick={onClose}>
          关闭
        </button>
      </div>
      <div className="dep-layer-tools">
        <input
          type="search"
          aria-label={`过滤${column.label}任务`}
          placeholder="按任务名过滤"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
        />
      </div>
      <div className="dep-layer-list">
        {filtered.length ? filtered.map((node) => {
          const run = node.latestRun;
          const inputs = inputsPreview(run?.inputs);
          return (
            <button
              key={node.id}
              type="button"
              className={`dep-layer-row${selectedId === node.id ? " selected" : ""}`}
              onClick={() => onSelect(node.id)}
              title={`${node.name} · ${directionLabel(node)}`}
            >
              <span className="dep-layer-row-name">
                {node.name}
                {node.archivedAt ? <span className="dep-graph-tag">已归档</span> : null}
              </span>
              <span className="dep-layer-row-status">
                <StatusInline state={run?.state} />
              </span>
              <span className="dep-layer-row-run">
                {run ? (
                  <>
                    #{shortId(run.id)} · {run.timeHour}
                  </>
                ) : (
                  "暂无运行"
                )}
              </span>
              <span className="dep-layer-row-param" title={inputs ?? "无参数"}>
                {run ? `${launchModeLabel(run.launchMode)} · 参数 ${inputs ?? "—"}` : "—"}
              </span>
            </button>
          );
        }) : (
          <p className="muted dep-layer-empty">没有匹配的任务。</p>
        )}
      </div>
    </section>
  );
}

export function DependencyGraphCanvas({
  graph,
  selectedId,
  onSelect,
}: {
  graph: DependencyGraph;
  selectedId: string | null;
  onSelect: (id: string | null) => void;
}) {
  const layout = useMemo(() => buildLayout(graph), [graph]);
  const [zoom, setZoom] = useState(1);
  const [hoveredId, setHoveredId] = useState<string | null>(null);
  const [openLayer, setOpenLayer] = useState<number | null>(null);
  const viewportRef = useRef<HTMLDivElement>(null);
  const detailRef = useRef<HTMLDivElement>(null);
  const markerId = `dep-arrow-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  const selected = selectedId ? graph.nodes.find((node) => node.id === selectedId) : undefined;
  const activeId = hoveredId ?? selectedId;
  const openColumn = openLayer === null ? undefined : layout.columns.find((column) => column.key === openLayer);
  const openNodes = openLayer === null ? [] : layout.allByColumn.get(openLayer) ?? [];
  const showLayerNav = layout.columns.length > 3;
  const centeredRef = useRef(false);

  useEffect(() => {
    setOpenLayer(null);
    centeredRef.current = false;
  }, [graph]);

  useEffect(() => {
    if (!selectedId || !detailRef.current) return;
    const frame = requestAnimationFrame(() => {
      detailRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    });
    return () => cancelAnimationFrame(frame);
  }, [selectedId]);

  // With 200 nodes a layered graph is still taller than the viewport. Start at
  // the current task instead of at an arbitrary empty top-left corner.
  useEffect(() => {
    if (centeredRef.current) return;
    const viewport = viewportRef.current;
    const centerNode = graph.nodes.find((node) => node.direction === "center");
    const position = centerNode ? layout.byId.get(centerNode.id) : undefined;
    if (!viewport || !position) return;
    viewport.scrollTop = Math.max(0, position.y * zoom - viewport.clientHeight / 2 + (position.height * zoom) / 2);
    viewport.scrollLeft = Math.max(0, position.x * zoom - viewport.clientWidth / 2 + (position.width * zoom) / 2);
    centeredRef.current = true;
  }, [graph, layout, zoom]);

  const fit = () => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const next = Math.min(
      (viewport.clientWidth - 16) / Math.max(1, layout.width),
      (viewport.clientHeight - 16) / Math.max(1, layout.height),
      1,
    );
    setZoom(Math.max(0.4, Math.round(next * 100) / 100));
  };
  const zoomBy = (delta: number) => setZoom((value) => Math.min(1.5, Math.max(0.4, Math.round((value + delta) * 100) / 100)));
  const scrollToColumn = (columnKey: number) => {
    const viewport = viewportRef.current;
    const column = layout.columns.find((item) => item.key === columnKey);
    if (!viewport || !column) return;
    viewport.scrollTo({ left: Math.max(0, column.x * zoom - 8), behavior: "smooth" });
  };

  return (
    <div className="dep-graph-shell">
      <div className="dep-graph-toolbar">
        <div className="dep-graph-legend" aria-label="图例">
          <span className="dep-legend-title">状态</span>
          {(["SUCCEEDED", "FAILED", "RUNNING", "QUEUED"] as const).map((state) => (
            <span className="dep-legend-item" key={state}>
              <span className={`dep-legend-dot dep-status-dot-${state}`} aria-hidden="true" />
              {stateLabel(state)}
            </span>
          ))}
          <span className="dep-legend-item">
            <span className="dep-legend-line" aria-hidden="true" />必选依赖
          </span>
          <span className="dep-legend-item">
            <span className="dep-legend-line dashed" aria-hidden="true" />总是触发
          </span>
        </div>
        <div className="dep-graph-zoom" role="group" aria-label="图谱缩放">
          <button className="btn small" type="button" aria-label="缩小依赖图" onClick={() => zoomBy(-0.1)}>
            −
          </button>
          <span className="dep-zoom-label">{Math.round(zoom * 100)}%</span>
          <button className="btn small" type="button" aria-label="放大依赖图" onClick={() => zoomBy(0.1)}>
            +
          </button>
          <button className="btn small" type="button" onClick={fit}>
            适配
          </button>
          <button className="btn small" type="button" onClick={() => setZoom(1)}>
            100%
          </button>
        </div>
      </div>

      {showLayerNav ? (
        <div className="dep-layer-nav" aria-label="跳转依赖层">
          {layout.columns.map((column) => (
            <button key={`nav-${column.key}`} className="dep-layer-nav-item" type="button" onClick={() => scrollToColumn(column.key)}>
              <span>{column.label}</span>
              <span className="dep-layer-nav-count">{column.count}</span>
            </button>
          ))}
        </div>
      ) : null}

      <div className="dep-graph-viewport" ref={viewportRef} role="group" aria-label="上下游依赖关系图">
        <div className="dep-graph-stage" style={{ width: layout.width * zoom, height: layout.height * zoom }}>
          <div
            className="dep-graph-inner"
            style={{ width: layout.width, height: layout.height, transform: `scale(${zoom})`, transformOrigin: "0 0" }}
          >
            {layout.columns.map((column) => (
              <div
                key={column.key}
                className={`dep-graph-lane${column.center ? " center" : ""}`}
                style={{ left: column.x, width: COLUMN_WIDTH, height: layout.height - HEADER_HEIGHT, top: HEADER_HEIGHT }}
                aria-hidden="true"
              />
            ))}
            {showLayerNav ? null : layout.columns.map((column) => (
              <div
                key={`label-${column.key}`}
                className={`dep-graph-column-label${column.center ? " center" : ""}`}
                style={{ left: column.x, width: COLUMN_WIDTH }}
              >
                <span>{column.label}</span>
                <span className="dep-column-count">{column.count}</span>
              </div>
            ))}
            <svg
              className="dep-graph-edges"
              width={layout.width}
              height={layout.height}
              aria-hidden="true"
              focusable="false"
            >
              <defs>
                <marker id={markerId} markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
                  <path d="M0,0 L8,4 L0,8 z" fill="#8a9aaa" />
                </marker>
              </defs>
              {graph.edges.map((edge: DependencyGraphEdge) => {
                const source = layout.byId.get(edge.upstreamJobId);
                const target = layout.byId.get(edge.downstreamJobId);
                if (!source || !target) return null;
                const active = activeId === edge.upstreamJobId || activeId === edge.downstreamJobId;
                const klass = [
                  "dep-graph-edge",
                  edge.condition === "always" ? "always" : "",
                  edge.required ? "" : "optional",
                  active ? "active" : "",
                ]
                  .filter(Boolean)
                  .join(" ");
                return (
                  <path key={`${edge.upstreamJobId}-${edge.downstreamJobId}`} d={edgePath(source, target)} className={klass} markerEnd={`url(#${markerId})`}>
                    <title>
                      {source.name} → {target.name} · {conditionLabel(edge.condition)}
                      {edge.required ? "" : " · 可选"}
                    </title>
                  </path>
                );
              })}
            </svg>
            {layout.nodes.map((node) => {
              const run = node.latestRun;
              const inputs = inputsPreview(run?.inputs);
              const isSelected = selectedId === node.id;
              return (
                <button
                  key={node.id}
                  type="button"
                  className={[
                    "dep-graph-node",
                    node.direction === "center" ? "center" : "",
                    isSelected ? "selected" : "",
                    statusClass(node),
                  ]
                    .filter(Boolean)
                    .join(" ")}
                  style={{ left: node.x, top: node.y, width: node.width, height: node.height }}
                  aria-pressed={isSelected}
                  title={`${node.name} · ${directionLabel(node)}`}
                  onClick={() => onSelect(isSelected ? null : node.id)}
                  onMouseEnter={() => setHoveredId(node.id)}
                  onMouseLeave={() => setHoveredId(null)}
                  onFocus={() => setHoveredId(node.id)}
                  onBlur={() => setHoveredId(null)}
                >
                  <span className="dep-graph-node-head">
                    <span className="dep-graph-node-name">{node.name}</span>
                    <StatusInline state={run?.state} />
                  </span>
                  <span className="dep-graph-node-meta">
                    {node.direction === "center" ? <span className="dep-graph-current">当前</span> : null}
                    {node.archivedAt ? <span className="dep-graph-tag">已归档</span> : null}
                    {run ? (
                      <>
                        <span>#{shortId(run.id)}</span>
                        <span>{run.timeHour}</span>
                      </>
                    ) : (
                      <span>暂无运行</span>
                    )}
                  </span>
                  {run ? (
                    <span className="dep-graph-node-param">
                      <span>{launchModeLabel(run.launchMode)}</span>
                      <span title={inputs ?? "无参数"}>参数 {inputs ?? "—"}</span>
                    </span>
                  ) : null}
                  {run?.error ? (
                    <span className="dep-graph-node-error" title={run.error}>
                      {run.error}
                    </span>
                  ) : null}
                </button>
              );
            })}
            {layout.groups.map((group) => (
              <button
                key={`group-${group.columnKey}`}
                type="button"
                className="dep-graph-group"
                style={{ left: group.x, top: group.y, width: group.width, height: group.height }}
                aria-label={`${group.label}还有 ${group.hidden} 个任务，查看全部`}
                onClick={() => setOpenLayer(group.columnKey)}
              >
                <span className="dep-group-title">还有 {group.hidden} 个</span>
                <span className="dep-group-summary">{stateSummary(group.counts)}</span>
                <span className="dep-group-action">查看全部 →</span>
              </button>
            ))}
          </div>
        </div>
      </div>
      <p className="dep-graph-hint">
        点击节点查看完整最近参数；悬停节点会高亮相关依赖边。复杂层只预览问题优先的任务，点击“查看全部”展开该层列表。
      </p>
      {openColumn ? (
        <LayerSheet
          column={openColumn}
          nodes={openNodes}
          selectedId={selectedId}
          onSelect={(id) => {
            onSelect(id);
            setOpenLayer(null);
          }}
          onClose={() => setOpenLayer(null)}
        />
      ) : null}
      <div ref={detailRef}>
        {selected ? <NodeDetail node={selected} onClose={() => onSelect(null)} /> : null}
      </div>
    </div>
  );
}
