import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useOutletContext } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getDependencyGraph, getJobs, setDependencies } from "../api/client";
import type { DependencyEdge } from "../api/types";
import { conditionLabel } from "../lib/format";
import { DependencyGraphCanvas } from "../components/DependencyGraph";
import { useToast } from "../components/Toast";
import { EmptyPanel } from "../components/PageBits";
import type { JobContext } from "./JobPage";

type SelectedEdge = {
  upstreamJobId: string;
  upstreamName: string;
  condition: "on_success" | "always";
  required: boolean;
};

function DependencyGraphPanel({ jobId }: { jobId: string }) {
  const [depth, setDepth] = useState(3);
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const query = useQuery({
    queryKey: ["dependency-graph", jobId, depth],
    queryFn: () => getDependencyGraph(jobId, depth),
  });
  const nodes = query.data?.nodes ?? [];
  const upstreamCount = nodes.filter((node) => node.direction === "upstream").length;
  const downstreamCount = nodes.filter((node) => node.direction === "downstream").length;

  useEffect(() => {
    setSelectedNodeId(null);
  }, [depth, jobId]);

  useEffect(() => {
    if (selectedNodeId && !nodes.some((node) => node.id === selectedNodeId)) {
      setSelectedNodeId(null);
    }
  }, [nodes, selectedNodeId]);

  return (
    <div className="pane dep-graph-pane">
      <div className="pane-header">
        <span>依赖全景</span>
        <span className="pane-note">
          图中 {upstreamCount} 个上游 · {downstreamCount} 个下游
        </span>
        <label className="inline-field dep-depth">
          <span className="muted small">上下游层数</span>
          <select
            className="small-select"
            aria-label="依赖展示层数"
            value={depth}
            onChange={(event) => setDepth(Number(event.target.value))}
          >
            <option value={3}>3 层（默认）</option>
            <option value={5}>5 层</option>
          </select>
        </label>
        <button
          className="btn small"
          type="button"
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {query.isFetching ? "刷新中…" : "刷新"}
        </button>
      </div>
      <div className="pane-body">
        {query.isPending ? (
          <p className="muted">正在加载依赖全景…</p>
        ) : query.isError ? (
          <p className="form-error" role="alert">
            依赖全景加载失败：{query.error.message}
          </p>
        ) : (
          <>
            {query.data.truncated ? (
              <p className="dep-graph-note" role="status">
                依赖节点达到或超过 200 个，当前只展示最靠近当前任务的部分；可以缩小层数查看。
              </p>
            ) : null}
            <DependencyGraphCanvas graph={query.data} selectedId={selectedNodeId} onSelect={setSelectedNodeId} />
          </>
        )}
      </div>
    </div>
  );
}

export function JobDependenciesView() {
  const { job, deps } = useOutletContext<JobContext>();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [selected, setSelected] = useState<SelectedEdge[]>([]);
  const [searchTerm, setSearchTerm] = useState("");
  const [submittedSearch, setSubmittedSearch] = useState("");
  const [error, setError] = useState("");
  const archived = Boolean(job.archivedAt);

  useEffect(() => {
    setSelected(
      deps.upstreams.map((edge) => ({
        upstreamJobId: edge.upstreamJobId ?? "",
        upstreamName: edge.upstreamName ?? edge.upstreamJobId ?? "",
        condition: edge.condition === "always" ? "always" : "on_success",
        required: edge.required,
      })),
    );
  }, [deps.upstreams]);

  const searchQuery = useQuery({
    queryKey: ["job-search", submittedSearch],
    queryFn: () => getJobs(submittedSearch),
    enabled: submittedSearch.length > 0,
  });

  const selectedIds = useMemo(() => new Set(selected.map((edge) => edge.upstreamJobId)), [selected]);

  const candidates = (searchQuery.data ?? []).filter(
    (candidate) => candidate.id !== job.id && !selectedIds.has(candidate.id),
  );

  const save = useMutation({
    mutationFn: () =>
      setDependencies(
        job.id,
        selected.map((edge) => ({
          upstreamJobId: edge.upstreamJobId,
          condition: edge.condition,
          required: edge.required,
        })),
      ),
    onSuccess: () => {
      setError("");
      toast.show("依赖已保存。", "success");
      void queryClient.invalidateQueries({ queryKey: ["dependencies", job.id] });
      void queryClient.invalidateQueries({ queryKey: ["dependency-graph", job.id] });
      void queryClient.invalidateQueries({ queryKey: ["jobs-overview"] });
    },
    onError: (err) => setError(err.message),
  });

  const search = (event: FormEvent) => {
    event.preventDefault();
    setSubmittedSearch(searchTerm.trim());
  };

  return (
    <>
      {archived ? (
        <div className="archive-banner" role="status">
          任务已归档：依赖关系已移除，且不能再编辑依赖。
        </div>
      ) : null}
      <DependencyGraphPanel jobId={job.id} />
      <div className="pane">
        <div className="pane-header">上游任务</div>
        <div className="pane-body">
          <p className="muted">
            所有必选上游都必须有相同 <code>time_hour</code> 的 <code>SUCCEEDED</code>{" "}
            运行；可选上游允许缺失。
          </p>
          {selected.length ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>上游任务</th>
                    <th>触发条件</th>
                    <th>必选</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {selected.map((edge, index) => (
                    <tr key={edge.upstreamJobId}>
                      <td>
                        <Link to={`/job/${edge.upstreamJobId}`}>{edge.upstreamName}</Link>
                      </td>
                      <td>
                        <select
                          aria-label={`${edge.upstreamName} 的触发条件`}
                          value={edge.condition}
                          disabled={archived}
                          onChange={(event) =>
                            setSelected((edges) =>
                              edges.map((item, itemIndex) =>
                                itemIndex === index
                                  ? {
                                      ...item,
                                      condition: event.target.value === "always" ? "always" : "on_success",
                                    }
                                  : item,
                              ),
                            )
                          }
                        >
                          <option value="on_success">上游成功时</option>
                          <option value="always">总是</option>
                        </select>
                      </td>
                      <td>
                        <input
                          type="checkbox"
                          aria-label={`${edge.upstreamName} 是否必选`}
                          checked={edge.required}
                          disabled={archived}
                          onChange={(event) =>
                            setSelected((edges) =>
                              edges.map((item, itemIndex) =>
                                itemIndex === index ? { ...item, required: event.target.checked } : item,
                              ),
                            )
                          }
                        />
                      </td>
                      <td>
                        <button
                          className="btn small danger"
                          type="button"
                          disabled={archived}
                          onClick={() =>
                            setSelected((edges) => edges.filter((_, itemIndex) => itemIndex !== index))
                          }
                        >
                          移除
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyPanel
              title="暂无上游任务"
              message="该任务可以独立运行。"
            />
          )}
          <p className="form-error" role="alert">
            {error}
          </p>
          <div className="form-actions">
            <button
              className="btn primary"
              type="button"
              disabled={archived || save.isPending}
              onClick={() => save.mutate()}
            >
              {save.isPending ? "保存中…" : "保存依赖"}
            </button>
          </div>
        </div>
      </div>

      {archived ? null : (
      <div className="pane">
        <div className="pane-header">添加上游</div>
        <div className="pane-body">
          <form className="inline-form" onSubmit={search}>
            <div className="field grow">
              <label htmlFor="dep-search">查找任务</label>
              <input
                id="dep-search"
                type="search"
                placeholder="按任务名称搜索"
                autoComplete="off"
                value={searchTerm}
                onChange={(event) => setSearchTerm(event.target.value)}
              />
            </div>
            <button type="submit" className="btn">
              搜索
            </button>
          </form>
          {submittedSearch ? (
            searchQuery.isPending ? (
              <p className="muted">搜索中…</p>
            ) : candidates.length ? (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>任务</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {candidates.map((candidate) => (
                      <tr key={candidate.id}>
                        <td>{candidate.name}</td>
                        <td>
                          <button
                            className="btn small"
                            type="button"
                            onClick={() =>
                              setSelected((edges) => [
                                ...edges,
                                {
                                  upstreamJobId: candidate.id,
                                  upstreamName: candidate.name,
                                  condition: "on_success",
                                  required: true,
                                },
                              ])
                            }
                          >
                            添加
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p className="muted">没有可添加的匹配任务。</p>
            )
          ) : (
            <p className="muted">搜索后添加一个上游任务。</p>
          )}
        </div>
      </div>
      )}

      <div className="pane">
        <div className="pane-header">下游任务（{deps.downstreams.length}）</div>
        <div className="pane-body">
          {deps.downstreams.length ? (
            <div className="table-wrap dep-direct-table">
              <table>
                <thead>
                  <tr>
                    <th>任务</th>
                    <th>触发条件</th>
                    <th>必选</th>
                  </tr>
                </thead>
                <tbody>
                  {deps.downstreams.map((edge: DependencyEdge) => (
                    <tr key={edge.downstreamJobId ?? edge.downstreamName}>
                      <td>
                        {edge.downstreamJobId ? (
                          <Link to={`/job/${edge.downstreamJobId}`}>{edge.downstreamName}</Link>
                        ) : (
                          edge.downstreamName
                        )}
                      </td>
                      <td>{conditionLabel(edge.condition)}</td>
                      <td>{edge.required ? "是" : "否"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="muted">暂无下游任务依赖此任务。</p>
          )}
        </div>
      </div>
    </>
  );
}