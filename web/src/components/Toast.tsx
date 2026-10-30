import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

type ToastTone = "info" | "success" | "error";

interface ToastMessage {
  id: number;
  text: string;
  tone: ToastTone;
}

interface ToastValue {
  show: (text: string, tone?: ToastTone) => void;
}

const ToastContext = createContext<ToastValue | undefined>(undefined);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [message, setMessage] = useState<ToastMessage | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const nextId = useRef(1);

  const show = useCallback((text: string, tone: ToastTone = "info") => {
    window.clearTimeout(timer.current);
    setMessage({ id: nextId.current++, text, tone });
    timer.current = window.setTimeout(() => setMessage(null), 4200);
  }, []);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const value = useMemo<ToastValue>(() => ({ show }), [show]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        className={`toast${message ? " show" : ""}${message?.tone === "error" ? " error" : ""}${
          message?.tone === "success" ? " success" : ""
        }`}
        role="status"
        aria-live="polite"
      >
        {message?.text ?? ""}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastValue {
  const value = useContext(ToastContext);
  if (!value) throw new Error("useToast must be used inside ToastProvider");
  return value;
}