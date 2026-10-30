import type { ReactNode } from "react";
import { Link } from "react-router-dom";

export function PageHeader({
  title,
  actions,
  titleAccessory,
}: {
  title: ReactNode;
  actions?: ReactNode;
  titleAccessory?: ReactNode;
}) {
  return (
    <div className="page-head">
      <h1>
        {titleAccessory}
        <span className="job-title">{title}</span>
      </h1>
      {actions ? <div className="page-actions">{actions}</div> : null}
    </div>
  );
}

export function Breadcrumbs({ children }: { children: ReactNode }) {
  return <div className="breadcrumbs">{children}</div>;
}

export function CrumbLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <>
      <Link to={to}>{children}</Link>
      <span className="sep">/</span>
    </>
  );
}

export function LoadingPanel({ label = "加载中…" }: { label?: string }) {
  return (
    <div className="loading">
      <span className="spinner" aria-hidden="true" />
      {label}
    </div>
  );
}

export function EmptyPanel({
  title,
  message,
  action,
}: {
  title: string;
  message: string;
  action?: ReactNode;
}) {
  return (
    <div className="empty">
      <h2>{title}</h2>
      <p>{message}</p>
      {action}
    </div>
  );
}

export function ErrorPanel({
  title = "页面加载失败",
  message,
  onRetry,
}: {
  title?: string;
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="empty" role="alert">
      <h2>{title}</h2>
      <p>{message}</p>
      {onRetry ? (
        <button className="btn" type="button" onClick={onRetry}>
          重试
        </button>
      ) : null}
    </div>
  );
}

export function PageError({ error, retry }: { error: unknown; retry?: () => void }) {
  const message = error instanceof Error ? error.message : "未知错误";
  return <ErrorPanel message={message} onRetry={retry} />;
}