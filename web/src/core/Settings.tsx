import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  ApiError,
  Async,
  Badge,
  Button,
  Callout,
  Card,
  Dash,
  Field,
  Hint,
  Input,
  Page,
  PageHeader,
  Row,
  Stack,
  Table,
  Time,
  api,
  useSnapshot,
} from "@cc/ui";

type Credential = {
  id: string;
  kind: "api_key" | "oauth";
  provider: string;
  status: string;
  version: number;
  expiresAt: string | null;
  scopes: string[];
};

type Route = {
  logicalName: string;
  capabilities: string[];
  attemptPlan: { provider: string; model: string }[];
  healthy: boolean;
  lastError: string;
};

/** Credentials (with re-auth), read-only effective model routes. */
export function Settings() {
  const creds = useSnapshot<Credential[]>(
    useCallback((signal) => api.snapshot<Credential[]>("/api/admin/credentials", { signal }), []),
    { events: "core.credential.**" },
  );
  const routes = useSnapshot<Route[]>(
    useCallback((signal) => api.snapshot<Route[]>("/api/admin/ai/routes", { signal }), []),
    { events: "core.ai.**" },
  );

  const [oauthFlash, setOauthFlash] = useState<{ tone: "ok" | "danger"; text: string } | null>(null);
  useEffect(() => {
    const q = new URLSearchParams(window.location.search);
    const oauth = q.get("oauth");
    if (oauth === "ok") setOauthFlash({ tone: "ok", text: "OAuth credential saved." });
    if (oauth === "error") {
      setOauthFlash({ tone: "danger", text: "OAuth did not complete. Begin again from Settings." });
    }
    if (oauth) {
      q.delete("oauth");
      const next = q.toString();
      window.history.replaceState(null, "", next ? `/settings?${next}` : "/settings");
    }
  }, []);

  return (
    <Page>
      <PageHeader
        title="Settings"
        lede="Credentials, reauthentication, and the effective model routes."
      />
      <Stack>
        {oauthFlash ? <Callout tone={oauthFlash.tone}>{oauthFlash.text}</Callout> : null}

        <ReauthCard />
        <CreateKeyCard onChanged={creds.reload} />
        <OAuthCard onChanged={creds.reload} />

        <div className="cc-group__title">Credentials</div>
        <Async
          state={creds}
          loading="Loading credentials…"
          empty="No credentials yet. Create an API key or complete OAuth."
        >
          {(list) => (
            <Stack>
              {list.map((c) => (
                <CredentialCard key={c.id} cred={c} onChanged={creds.reload} />
              ))}
            </Stack>
          )}
        </Async>

        <Card title="Model routes">
          <Async
            state={routes}
            loading="Loading routes…"
            empty="No routes in models.yaml. Add a route and restart after creating its credential."
          >
            {(list) => (
              <Table
                head={
                  <>
                    <th>Logical name</th>
                    <th>Capabilities</th>
                    <th>Attempts</th>
                    <th>Health</th>
                  </>
                }
              >
                {list.map((r) => (
                  <tr key={r.logicalName}>
                    <td>
                      <code>{r.logicalName}</code>
                    </td>
                    <td>{r.capabilities.join(", ") || <Dash />}</td>
                    <td>
                      <code>{r.attemptPlan.map((a) => `${a.provider}/${a.model}`).join(" → ")}</code>
                    </td>
                    <td>
                      <Badge tone={r.healthy ? "ok" : "danger"}>
                        {r.healthy ? "healthy" : "unhealthy"}
                      </Badge>
                      {!r.healthy && r.lastError ? <Hint>{r.lastError}</Hint> : null}
                    </td>
                  </tr>
                ))}
              </Table>
            )}
          </Async>
        </Card>
      </Stack>
    </Page>
  );
}

function isReauth(err: unknown): boolean {
  return err instanceof ApiError && err.code === "reauth_required";
}

function formatErr(err: unknown): string {
  return err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err);
}

/** The one place a mutation card says "reauthenticate first", worded the same way. */
function ReauthNotice() {
  return <Callout tone="warn">Reauthenticate above, then try again.</Callout>;
}

function ReauthCard() {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setOk(false);
    try {
      await api.post("/api/auth/reauth", { password });
      setPassword("");
      setOk(true);
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Reauthenticate">
      <form onSubmit={onSubmit}>
        <Stack>
          <Hint>
            Credential changes require the administrator password within the last five minutes.
          </Hint>
          {error ? <Callout tone="danger">{error}</Callout> : null}
          {ok ? <Callout tone="ok">Reauthenticated. Mutations are allowed for five minutes.</Callout> : null}
          <Field label="Password">
            <Input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </Field>
          <Row>
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? "Confirming…" : "Confirm password"}
            </Button>
          </Row>
        </Stack>
      </form>
    </Card>
  );
}

function CreateKeyCard({ onChanged }: { onChanged: () => void }) {
  const [id, setId] = useState("");
  const [provider, setProvider] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needReauth, setNeedReauth] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setNeedReauth(false);
    try {
      await api.post("/api/admin/credentials", { id, provider, secret });
      setId("");
      setProvider("");
      setSecret("");
      onChanged();
    } catch (err) {
      if (isReauth(err)) setNeedReauth(true);
      else setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Create API key">
      <form onSubmit={onSubmit}>
        <Stack>
          {needReauth ? <ReauthNotice /> : null}
          {error ? <Callout tone="danger">{error}</Callout> : null}
          <Field label="ID" hint="Lowercase letters, digits, hyphen, underscore.">
            <Input mono value={id} onChange={(e) => setId(e.target.value)} required />
          </Field>
          <Field label="Provider">
            <Input mono value={provider} onChange={(e) => setProvider(e.target.value)} required />
          </Field>
          <Field label="Secret" hint="Never displayed again after you save.">
            <Input
              type="password"
              autoComplete="new-password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              required
            />
          </Field>
          <Row>
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? "Creating…" : "Create"}
            </Button>
          </Row>
        </Stack>
      </form>
    </Card>
  );
}

function OAuthCard({ onChanged }: { onChanged: () => void }) {
  const [providers, setProviders] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needReauth, setNeedReauth] = useState(false);

  useEffect(() => {
    let cancelled = false;
    api.get<string[]>("/api/admin/credentials/oauth/providers").then(
      (list) => {
        if (!cancelled) setProviders(list);
      },
      () => {
        if (!cancelled) setProviders([]);
      },
    );
    return () => {
      cancelled = true;
    };
  }, []);

  if (providers.length === 0) return null;

  const begin = async (provider: string) => {
    setBusy(true);
    setError(null);
    setNeedReauth(false);
    try {
      const res = await api.post<{ authUrl: string }>(
        `/api/admin/credentials/oauth/${encodeURIComponent(provider)}/begin`,
      );
      onChanged();
      window.location.assign(res.authUrl);
    } catch (err) {
      if (isReauth(err)) setNeedReauth(true);
      else setError(formatErr(err));
      setBusy(false);
    }
  };

  return (
    <Card title="OAuth">
      <Stack>
        {needReauth ? <ReauthNotice /> : null}
        {error ? <Callout tone="danger">{error}</Callout> : null}
        <Hint>The callback never shows tokens. State is bound to this session.</Hint>
        <Row>
          {providers.map((p) => (
            <Button key={p} type="button" disabled={busy} onClick={() => void begin(p)}>
              Connect {p}
            </Button>
          ))}
        </Row>
      </Stack>
    </Card>
  );
}

function CredentialCard({ cred, onChanged }: { cred: Credential; onChanged: () => void }) {
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needReauth, setNeedReauth] = useState(false);
  const [refs, setRefs] = useState<string[] | null>(null);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    setNeedReauth(false);
    try {
      await fn();
      setSecret("");
      onChanged();
    } catch (err) {
      if (isReauth(err)) setNeedReauth(true);
      else setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    let cancelled = false;
    api.get<string[]>(`/api/admin/credentials/${encodeURIComponent(cred.id)}/references`).then(
      (list) => {
        if (!cancelled) setRefs(list);
      },
      () => {
        if (!cancelled) setRefs([]);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [cred.id, cred.version]);

  const referenced = refs !== null && refs.length > 0;

  return (
    <Card
      title={cred.id}
      actions={
        <>
          <Badge>{cred.kind}</Badge>
          <Badge>{cred.provider}</Badge>
          <Badge tone={cred.status === "ok" ? "ok" : "danger"}>{cred.status}</Badge>
        </>
      }
    >
      <Stack>
        <Hint>
          Version {cred.version}
          {cred.expiresAt ? (
            <>
              {" · expires "}
              <Time iso={cred.expiresAt} />
            </>
          ) : null}
          {cred.scopes.length > 0 ? ` · ${cred.scopes.join(", ")}` : ""}
        </Hint>
        {referenced ? (
          <Hint>Referenced by {refs.join(", ")}. Remove those before deleting.</Hint>
        ) : null}
        {needReauth ? <ReauthNotice /> : null}
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {cred.kind === "api_key" ? (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void act(() => api.put(`/api/admin/credentials/${encodeURIComponent(cred.id)}`, { secret }));
            }}
          >
            <Stack>
              <Field label="Replacement secret" hint="Never displayed after save.">
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                  required
                />
              </Field>
              <Row>
                <Button type="submit" disabled={busy}>
                  Replace
                </Button>
                <Button
                  type="button"
                  disabled={busy}
                  onClick={() =>
                    void act(() =>
                      api.post(`/api/admin/credentials/${encodeURIComponent(cred.id)}/rotate`, { secret }),
                    )
                  }
                >
                  Rotate
                </Button>
              </Row>
            </Stack>
          </form>
        ) : null}
        <Row>
          <Button
            type="button"
            variant="danger"
            disabled={busy || referenced}
            title={referenced ? "Remove the references above before deleting." : undefined}
            onClick={() => void act(() => api.del(`/api/admin/credentials/${encodeURIComponent(cred.id)}`))}
          >
            Delete
          </Button>
        </Row>
      </Stack>
    </Card>
  );
}
