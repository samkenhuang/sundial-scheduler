# Scheduler Console (web)

React 19 + TypeScript + Vite frontend for the scheduler control plane. Jenkins
is the visual reference; this is a normal SPA, not a Jenkins plugin. The UI
defaults to Simplified Chinese (`zh-CN`).

## Commands

```bash
npm ci              # install from the lockfile
npm run dev         # Vite :5173, proxies /v1 to http://127.0.0.1:8080
npm run typecheck   # tsc --noEmit
npm run build       # type-check + Vite production build
```

The build writes to `../internal/web/dist`. The Go API embeds that directory and
serves it at `/ui/`; there is no separate production frontend server.

For the full local flow from the repository root:

```bash
make web-dev        # same Vite dev server
make web-build      # build assets for the embedded binary
make run-server     # API at :8080, console at /ui/
```

## Structure

- `src/api` — typed `/v1` client, bearer-token handling, query functions.
- `src/auth` — token state in `localStorage` and 401 recovery.
- `src/components` — shell, status ball, accessible dialog (focus trap/Escape),
  toast, page primitives.
- `src/pages` — dashboard, new item/project, job/config/dependencies, run
  console, queue, history, workers, API.
- `src/styles.css` — Jenkins-classic dense-table theme.

`Vite` uses `base: "/ui/"`, and the Go handler falls back to `index.html` for
extensionless paths so clean routes such as `/ui/job/{id}` survive a refresh.

The browser sends `Authorization: Bearer <SCHED_DEV_TOKEN>` on every API call.
This is a P0 development console; token storage is not an identity system.
