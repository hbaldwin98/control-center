/** The unauthenticated screens: first-run setup and login. */
import { useState, type FormEvent, type ReactNode } from "react";
import { ApiError, Button, Callout, Field, Input, Stack, api, setCsrfToken } from "@cc/ui";
import { useSession } from "./session";

function Gate({ title, lede, children }: { title: string; lede: string; children: ReactNode }) {
  return (
    <div className="cc-gate">
      <div className="cc-gate__panel">
        <h1 className="cc-gate__title">{title}</h1>
        <p className="cc-gate__lede">{lede}</p>
        {children}
      </div>
    </div>
  );
}

function useSubmit(action: () => Promise<void>) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await action();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, onSubmit };
}

type SessionResponse = { csrfToken: string; expiresAt: string };

/**
 * First-run setup. The one-time token is printed to the server log; the endpoint accepts
 * it only from a loopback peer and only while no administrator exists.
 */
export function SetupScreen({ available }: { available: boolean }) {
  const { refresh } = useSession();
  const [token, setToken] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");

  const { busy, error, onSubmit } = useSubmit(async () => {
    if (password !== confirm) throw new ApiError(400, "bad_request", "Passwords do not match.");
    const res = await api.post<SessionResponse>("/api/auth/bootstrap", { token, password });
    setCsrfToken(res.csrfToken);
    await refresh();
  });

  if (!available) {
    return (
      <Gate
        title="Set up Control Center"
        lede="First-run setup is available only from the machine running the server."
      >
        <Callout tone="danger">
          Open Control Center over loopback (for example{" "}
          <code>http://127.0.0.1:8080</code>) to complete setup.
        </Callout>
      </Gate>
    );
  }

  return (
    <Gate
      title="Set up Control Center"
      lede="Paste the one-time token from the server log and choose the administrator password."
    >
      <form onSubmit={onSubmit}>
        <Stack>
          <Field label="One-time token" hint="Printed by the server on startup.">
            <Input
              mono
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              required
            />
          </Field>
          <Field label="Administrator password" hint="At least 12 characters. There is no reset flow.">
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
              minLength={12}
              required
            />
          </Field>
          <Field label="Confirm password">
            <Input
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              autoComplete="new-password"
              required
            />
          </Field>
          {error ? <Callout tone="danger">{error}</Callout> : null}
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? "Setting up…" : "Complete setup"}
          </Button>
        </Stack>
      </form>
    </Gate>
  );
}

export function LoginScreen() {
  const { refresh } = useSession();
  const [password, setPassword] = useState("");

  const { busy, error, onSubmit } = useSubmit(async () => {
    const res = await api.post<SessionResponse>("/api/auth/login", { password });
    setCsrfToken(res.csrfToken);
    await refresh();
  });

  return (
    <Gate title="Control Center" lede="Sign in as the administrator.">
      <form onSubmit={onSubmit}>
        <Stack>
          <Field label="Password">
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              autoFocus
              required
            />
          </Field>
          {error ? <Callout tone="danger">{error}</Callout> : null}
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? "Signing in…" : "Sign in"}
          </Button>
        </Stack>
      </form>
    </Gate>
  );
}

export function FatalScreen({ error }: { error: Error }) {
  return (
    <Gate title="Control Center could not start" lede="The shell stopped before rendering.">
      <Callout tone="danger">
        <pre style={{ margin: 0, whiteSpace: "pre-wrap", fontSize: 12.5 }}>{error.message}</pre>
      </Callout>
    </Gate>
  );
}
