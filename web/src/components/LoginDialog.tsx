import { useEffect, useRef, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthProvider";

export function LoginDialog() {
  const { token, dialogOpen, dialogError, closeDialog, signIn } = useAuth();
  const [value, setValue] = useState(token ?? "");
  const [error, setError] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const modalRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!dialogOpen) return;
    setValue(token ?? "");
    setError("");
    const id = window.setTimeout(() => inputRef.current?.focus(), 30);
    return () => window.clearTimeout(id);
  }, [dialogOpen, token]);

  useEffect(() => {
    if (!dialogOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && token) {
        closeDialog();
        return;
      }
      if (event.key !== "Tab" || !modalRef.current) return;
      const focusable = Array.from(
        modalRef.current.querySelectorAll<HTMLElement>(
          'button:not([disabled]), input:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
        ),
      );
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [dialogOpen, token, closeDialog]);

  if (!dialogOpen) return null;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!value.trim()) {
      setError("请输入 API 令牌。");
      inputRef.current?.focus();
      return;
    }
    signIn(value);
  };

  return (
    <div
      className="modal-backdrop"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && token) closeDialog();
      }}
    >
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="login-title" ref={modalRef}>
        <div className="modal-logo">
          <span className="brand-mark" aria-hidden="true">
            S
          </span>
          Sundial 调度
        </div>
        <h1 id="login-title">API 令牌</h1>
        <p className="muted">
          输入服务端配置的 <code>SCHED_DEV_TOKEN</code> 开发令牌。该令牌只保存在当前浏览器。
        </p>
        <form onSubmit={submit}>
          <label htmlFor="token-input">Bearer 令牌</label>
          <input
            id="token-input"
            ref={inputRef}
            type="password"
            autoComplete="current-password"
            value={value}
            onChange={(event) => {
              setValue(event.target.value);
              setError("");
            }}
            aria-invalid={error ? "true" : undefined}
            aria-describedby={error ? "login-error" : undefined}
          />
          <p id="login-error" className="form-error" role="alert">
            {error || dialogError}
          </p>
          <div className="modal-actions">
            {token ? (
              <button type="button" className="btn" onClick={closeDialog}>
                取消
              </button>
            ) : null}
            <button type="submit" className="btn primary">
              登录
            </button>
          </div>
        </form>
        <p className="modal-help">
          每次 API 请求都会带上 <code>Authorization: Bearer …</code>。
        </p>
      </div>
    </div>
  );
}
