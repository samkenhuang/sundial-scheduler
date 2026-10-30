import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  clearToken,
  getToken,
  storeToken,
  UNAUTHORIZED_EVENT,
} from "../api/client";

interface AuthValue {
  token: string | null;
  dialogOpen: boolean;
  dialogError: string;
  openDialog: () => void;
  closeDialog: () => void;
  signIn: (token: string) => void;
  signOut: () => void;
}

const AuthContext = createContext<AuthValue | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [token, setToken] = useState<string | null>(() => getToken());
  const [dialogOpen, setDialogOpen] = useState(() => !getToken());
  const [dialogError, setDialogError] = useState("");

  useEffect(() => {
    const onUnauthorized = () => {
      clearToken();
      setToken(null);
      setDialogError("令牌被拒绝，请检查 SCHED_DEV_TOKEN 后重试。");
      setDialogOpen(true);
      queryClient.clear();
    };
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
  }, [queryClient]);

  const signIn = useCallback(
    (nextToken: string) => {
      const trimmed = nextToken.trim();
      if (!trimmed) return;
      storeToken(trimmed);
      setToken(trimmed);
      setDialogError("");
      setDialogOpen(false);
      queryClient.clear();
    },
    [queryClient],
  );

  const signOut = useCallback(() => {
    clearToken();
    setToken(null);
    setDialogOpen(true);
    queryClient.clear();
  }, [queryClient]);

  const openDialog = useCallback(() => {
    setDialogError("");
    setDialogOpen(true);
  }, []);
  const closeDialog = useCallback(() => {
    if (token) setDialogOpen(false);
  }, [token]);

  const value = useMemo<AuthValue>(
    () => ({ token, dialogOpen, dialogError, openDialog, closeDialog, signIn, signOut }),
    [token, dialogOpen, dialogError, openDialog, closeDialog, signIn, signOut],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthValue {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}