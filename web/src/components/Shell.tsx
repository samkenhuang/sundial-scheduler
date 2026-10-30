import { useEffect, useState, type FormEvent } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { getQueuedRuns, getWorkers } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { LoginDialog } from "./LoginDialog";
import { StatusBall } from "./StatusBall";
import { EmptyPanel } from "./PageBits";
import { RunningRunList } from "./RunningRuns";

const NAV_ITEMS = [
  { to: "/", label: "任务", icon: "▦", end: true },
  { to: "/new", label: "新建任务", icon: "＋" },
  { to: "/queue", label: "构建队列", icon: "≡" },
  { to: "/history", label: "构建历史", icon: "🕘" },
  { to: "/executors", label: "执行器", icon: "▤" },
  { to: "/control-plane", label: "控制面", icon: "◈" },
  { to: "/deploy", label: "部署", icon: "⚙" },
  { to: "/api", label: "REST API", icon: "↗" },
];

export function Shell() {
  const { token, openDialog, signOut } = useAuth();
  const location = useLocation();
  const navigate = useNavigate();
  const [navOpen, setNavOpen] = useState(false);
  const [search, setSearch] = useState("");

  useEffect(() => {
    setNavOpen(false);
  }, [location.pathname, location.search]);

  useEffect(() => {
    if (location.pathname === "/") {
      setSearch(new URLSearchParams(location.search).get("q") ?? "");
    }
  }, [location.pathname, location.search]);

  const workersQuery = useQuery({
    queryKey: ["workers"],
    queryFn: getWorkers,
    enabled: Boolean(token),
    refetchInterval: 10_000,
  });
  const queueQuery = useQuery({
    queryKey: ["queued-runs"],
    queryFn: getQueuedRuns,
    enabled: Boolean(token),
    refetchInterval: 20_000,
  });

  const workers = workersQuery.data ?? [];
  const online = workers.filter((worker) => worker.status === "ONLINE").length;
  const runningTotal = workers.reduce(
    (total, worker) => total + (worker.runningCount ?? worker.runningRuns?.length ?? 0),
    0,
  );
  const visibleWorkers = workers.slice(0, 4);
  const queued = queueQuery.data ?? [];

  const submitSearch = (event: FormEvent) => {
    event.preventDefault();
    const q = search.trim();
    navigate(q ? `/?q=${encodeURIComponent(q)}` : "/");
  };

  return (
    <>
      <a className="skip-link" href="#content">
        跳到主要内容
      </a>
      <header id="topbar">
        <button
          id="menu-toggle"
          className="menu-toggle"
          type="button"
          aria-label="切换导航"
          aria-expanded={navOpen}
          onClick={() => setNavOpen((open) => !open)}
        >
          ☰
        </button>
        <NavLink className="brand" to="/">
          <span className="brand-mark" aria-hidden="true">
            S
          </span>
          <span className="brand-name">Sundial 调度</span>
        </NavLink>
        <span className="brand-tag">控制面</span>
        <form id="search-form" className="search" role="search" onSubmit={submitSearch}>
          <label className="sr-only" htmlFor="search-input">
            搜索任务
          </label>
          <input
            id="search-input"
            type="search"
            placeholder="搜索任务"
            autoComplete="off"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
          <button type="submit" aria-label="搜索">
            ⌕
          </button>
        </form>
        <div className="top-actions">
          <span
            className={`connection${token ? "" : " bad"}`}
            title={token ? "已配置 API 令牌" : "未配置 API 令牌"}
            aria-label={token ? "已配置 API 令牌" : "未配置 API 令牌"}
          >
            ●
          </span>
          <button className="link-button" type="button" onClick={openDialog}>
            API 令牌
          </button>
          {token ? (
            <button className="link-button signout-link" type="button" onClick={signOut}>
              退出
            </button>
          ) : null}
        </div>
      </header>

      <div id="layout">
        <aside id="sidebar" className={navOpen ? "open" : undefined} aria-label="主导航">
          <nav className="nav">
            {NAV_ITEMS.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                className={({ isActive }) => (isActive ? "active" : undefined)}
              >
                <span className="nav-icon" aria-hidden="true">
                  {item.icon}
                </span>
                {item.label}
              </NavLink>
            ))}
          </nav>

          <section className="sidebar-box" aria-labelledby="queue-heading">
            <h2 id="queue-heading">
              <Link to="/queue">构建队列</Link>
            </h2>
            <div id="queue-state" className="sidebar-note">
              {!token ? (
                "登录后可见"
              ) : queued.length ? (
                <Link className="worker-line" to="/queue">
                  <StatusBall state="QUEUED" />
                  <span>
                    {queued.length}
                    {queued.length >= 50 ? "+" : ""} 个排队中
                  </span>
                </Link>
              ) : (
                "暂无排队构建"
              )}
            </div>
          </section>

          <section className="sidebar-box" aria-labelledby="executor-heading">
            <h2 id="executor-heading">
              <Link to="/executors">执行器</Link>
            </h2>
            <div id="executor-state" className="sidebar-note">
              {!token ? (
                "登录后可见"
              ) : workers.length ? (
                <>
                  <div className="worker-line">
                    <StatusBall state={online ? "SUCCEEDED" : "OFFLINE"} />
                    <span>
                      {online}/{workers.length} 在线
                    </span>
                    <span className="muted">{runningTotal} 运行中</span>
                  </div>
                  {visibleWorkers.map((worker) => {
                    const runs = worker.runningRuns ?? [];
                    const running = worker.runningCount ?? runs.length;
                    return (
                      <div className="sidebar-executor" key={worker.id}>
                        <div className="worker-line">
                          <StatusBall state={worker.status} />
                          <span
                            className="executor-host"
                            title={`${worker.name || worker.hostname || worker.id} · ${worker.hostname || worker.id}`}
                          >
                            {worker.name || worker.hostname || worker.id.slice(0, 8)}
                          </span>
                          <span
                            className="executor-load"
                            title={`运行中 ${running} / 并发上限 ${worker.concurrencyLimit}`}
                          >
                            {running}/{worker.concurrencyLimit}
                          </span>
                        </div>
                        <RunningRunList
                          runs={runs}
                          limit={2}
                          empty={worker.status === "OFFLINE" ? "离线" : "空闲"}
                          moreTo={`/executors/${worker.id}`}
                        />
                      </div>
                    );
                  })}
                  {workers.length > visibleWorkers.length ? (
                    <Link className="running-more" to="/executors">
                      查看全部 {workers.length} 个执行器
                    </Link>
                  ) : null}
                  <Link className="running-more" to="/executors/setup">
                    Worker 接入 / 下载 sched
                  </Link>
                </>
              ) : (
                "暂无执行器"
              )}
            </div>
          </section>

          <div className="sidebar-footer">
            <span>调度器 1.0</span>
            <NavLink to="/api">REST API</NavLink>
          </div>
        </aside>

        <main id="content" tabIndex={-1}>
          {token ? (
            <Outlet />
          ) : (
            <EmptyPanel
              title="登录调度平台"
              message="输入开发环境 Bearer 令牌以打开控制台。"
              action={
                <button className="btn primary" type="button" onClick={openDialog}>
                  API 令牌
                </button>
              }
            />
          )}
        </main>
      </div>

      <LoginDialog />
    </>
  );
}
