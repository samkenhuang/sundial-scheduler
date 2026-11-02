# 调度控制台（web）

调度控制面的 React 19 + TypeScript + Vite 前端。Jenkins 只作为视觉参考；这是一个普通 SPA，不是 Jenkins 插件。UI 默认简体中文（`zh-CN`）。

## 命令

```bash
npm ci              # 按 lockfile 安装依赖
npm run dev         # Vite :5173，把 /v1 代理到 http://127.0.0.1:8080
npm run typecheck   # tsc --noEmit
npm run build       # 类型检查 + Vite 生产构建
```

构建产物写入 `../internal/web/dist`。Go API 嵌入该目录并在 `/ui/` 下提供；没有独立的生产前端服务器。

在仓库根目录执行完整本地流程：

```bash
make web-dev        # 同上，启动 Vite dev server
make web-build      # 构建嵌入二进制所需的资源
make run-server     # API 监听 :8080，控制台在 /ui/
```

## 结构

- `src/api` — 带类型的 `/v1` 客户端、Bearer token 处理、查询函数。
- `src/auth` — `localStorage` 中的 token 状态和 401 恢复。
- `src/components` — 外壳、状态球、可访问对话框（焦点陷阱/Escape）、toast、页面基础组件。
- `src/pages` — 面板、新建任务/项目、任务/配置/依赖、运行控制台、队列、历史、执行器、API。
- `src/styles.css` — Jenkins 经典风格的密集表格主题。

`Vite` 使用 `base: "/ui/"`；Go handler 对无扩展名路径回退到 `index.html`，因此 `/ui/job/{id}` 这类干净路由在刷新后仍然可用。

浏览器每次调用 API 都会发送 `Authorization: Bearer <SCHED_DEV_TOKEN>`。这是 P0 开发控制台；token 存储不是身份系统。
