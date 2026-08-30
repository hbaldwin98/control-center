/**
 * Session and bootstrap state.
 *
 * The shell loads `/api/auth/status` to decide between first-run setup, login, and the
 * application, then loads `/api/bootstrap` — the snapshot whose `asOfEventId` is the
 * boundary the one shared event stream opens after.
 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { api, setCsrfToken, type PluginDescriptor, type Snapshot } from "@cc/ui";

export type AuthStatus = {
  bootstrapRequired: boolean;
  bootstrapAvailable: boolean;
  authenticated: boolean;
};

export type ShellBootstrap = {
  csrfToken: string;
  serverTime: string;
  session: { expiresAt: string; reauthAt: string | null };
  plugins: PluginDescriptor[];
};

type SessionState =
  | { phase: "loading" }
  | { phase: "setup"; available: boolean }
  | { phase: "login" }
  | { phase: "ready"; bootstrap: ShellBootstrap; asOfEventId: string }
  | { phase: "error"; error: Error };

type SessionContextValue = {
  state: SessionState;
  /** Re-reads auth status and, when authenticated, the bootstrap snapshot. */
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
};

const SessionContext = createContext<SessionContextValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<SessionState>({ phase: "loading" });

  const refresh = useCallback(async () => {
    try {
      const status = await api.get<AuthStatus>("/api/auth/status");
      if (status.bootstrapRequired) {
        setState({ phase: "setup", available: status.bootstrapAvailable });
        return;
      }
      if (!status.authenticated) {
        setState({ phase: "login" });
        return;
      }
      const snap: Snapshot<ShellBootstrap> = await api.snapshot<ShellBootstrap>("/api/bootstrap");
      setCsrfToken(snap.data.csrfToken);
      setState({ phase: "ready", bootstrap: snap.data, asOfEventId: snap.asOfEventId });
    } catch (err) {
      setState({ phase: "error", error: err instanceof Error ? err : new Error(String(err)) });
    }
  }, []);

  const logout = useCallback(async () => {
    try {
      await api.post("/api/auth/logout");
    } finally {
      setCsrfToken("");
      await refresh();
    }
  }, [refresh]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const value = useMemo(() => ({ state, refresh, logout }), [state, refresh, logout]);
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionContextValue {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used inside SessionProvider");
  return ctx;
}

/** The bootstrap snapshot, available only inside the authenticated tree. */
export function useBootstrap(): ShellBootstrap {
  const { state } = useSession();
  if (state.phase !== "ready") {
    throw new Error("useBootstrap must be used inside the authenticated shell");
  }
  return state.bootstrap;
}
