import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  ApiError,
  Async,
  Badge,
  Button,
  Callout,
  Card,
  Field,
  Hint,
  Input,
  Page,
  PageHeader,
  Row,
  Stack,
  Textarea,
  Time,
  api,
  useSnapshot,
} from "@cc/ui";
import { AlertSoundCard } from "./AlertSound";
import { NotificationSettings } from "./NotificationSettings";

type Credential = {
  id: string;
  kind: "api_key" | "oauth";
  provider: string;
  status: string;
  version: number;
  expiresAt: string | null;
  scopes: string[];
};

/**
 * An OAuth provider as the server describes it. `manual` means the provider pins a
 * redirect this deployment can never receive, so the administrator finishes the flow by
 * pasting back the URL their browser landed on.
 */
type OAuthProvider = {
  name: string;
  manual: boolean;
  redirectUri: string;
  scopes: string[];
  importable: boolean;
};

/** Credentials: the administrator password gate, API keys, and OAuth logins. */
export function Settings() {
  const creds = useSnapshot<Credential[]>(
    useCallback(
      (signal) =>
        api.snapshot<Credential[]>("/api/admin/credentials", { signal }),
      [],
    ),
    { events: "core.credential.**" },
  );

  const [oauthFlash, setOauthFlash] = useState<{
    tone: "ok" | "danger";
    text: string;
  } | null>(null);
  useEffect(() => {
    const q = new URLSearchParams(window.location.search);
    const oauth = q.get("oauth");
    if (oauth === "ok")
      setOauthFlash({ tone: "ok", text: "OAuth credential saved." });
    if (oauth === "error") {
      setOauthFlash({
        tone: "danger",
        text: "OAuth did not complete. Begin again from Settings.",
      });
    }
    if (oauth) {
      q.delete("oauth");
      const next = q.toString();
      window.history.replaceState(
        null,
        "",
        next ? `/settings?${next}` : "/settings",
      );
    }
  }, []);

  return (
    <Page>
      <PageHeader
        title="Settings"
        lede="API keys and logins. After you save a key, connect it as a provider under Models — that is what plugins use."
      />
      <Stack>
        {oauthFlash ? (
          <Callout tone={oauthFlash.tone}>{oauthFlash.text}</Callout>
        ) : null}

        <nav className="cc-jumpnav" aria-label="Settings sections">
          <a href="#settings-security">Security</a>
          <a href="#settings-credentials">Credentials</a>
          <a href="#settings-notifications">Notifications</a>
          <a href="#settings-preferences">Preferences</a>
        </nav>

        <div id="settings-security" className="cc-anchor-section">
          <ChangePasswordCard />
          <ReauthCard />
        </div>
        <div id="settings-credentials" className="cc-anchor-section">
          <OAuthCard onChanged={creds.reload} />
          <CreateKeyCard onChanged={creds.reload} />

          <div className="cc-group__title">Credentials</div>
          <Async
            state={creds}
            loading="Loading credentials…"
            empty="No credentials yet. Create an API key or complete OAuth."
          >
            {(list) => (
              <Stack>
                {list.map((c) => (
                  <CredentialCard
                    key={c.id}
                    cred={c}
                    onChanged={creds.reload}
                  />
                ))}
              </Stack>
            )}
          </Async>
        </div>
        <div id="settings-notifications" className="cc-anchor-section">
          <NotificationSettings />
        </div>
        <div id="settings-preferences" className="cc-anchor-section">
          <AlertSoundCard />
        </div>
      </Stack>
    </Page>
  );
}

function isReauth(err: unknown): boolean {
  return err instanceof ApiError && err.code === "reauth_required";
}

function formatErr(err: unknown): string {
  return err instanceof ApiError
    ? err.message
    : err instanceof Error
      ? err.message
      : String(err);
}

/** OAuth URLs come from the server, but still need a browser-side scheme guard. */
function checkedAuthURL(raw: string): string {
  let url: URL;
  try {
    url = new URL(raw, window.location.origin);
  } catch {
    throw new Error("The provider returned an invalid sign-in URL.");
  }
  const loopback = ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
  if (url.protocol !== "https:" && !(url.protocol === "http:" && loopback)) {
    throw new Error("The provider returned an unsafe sign-in URL.");
  }
  return url.toString();
}

/** The one place a mutation card says "reauthenticate first", worded the same way. */
// The id the notice focuses. "Reauthenticate above" is not enough on its own: another
// card sits between, and the password asked for is the administrator's own, not a secret
// belonging to whatever is being configured.
const reauthFieldID = "settings-reauth-password";

function ReauthNotice() {
  return (
    <Callout tone="warn">
      Confirm your administrator password under{" "}
      <a
        href={`#${reauthFieldID}`}
        onClick={(e) => {
          e.preventDefault();
          const field = document.getElementById(reauthFieldID);
          field?.scrollIntoView({ block: "center", behavior: "smooth" });
          field?.focus();
        }}
      >
        Reauthenticate
      </a>{" "}
      at the top of this page, then try again.
    </Callout>
  );
}

/**
 * The administrator password. There is no reset flow, so this is the only way to rotate
 * the one chosen at first-run setup. Unlike the credential cards this does not go through
 * the reauth window: the endpoint takes the current password itself, and signs out every
 * other session on success.
 */
function ChangePasswordCard() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setOk(false);
    if (next !== confirm) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      await api.post("/api/auth/password", {
        currentPassword: current,
        newPassword: next,
      });
      setCurrent("");
      setNext("");
      setConfirm("");
      setOk(true);
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Administrator password">
      <form onSubmit={onSubmit}>
        <Stack>
          <Hint>
            Changing the password signs out every other device. This session
            stays signed in.
          </Hint>
          {error ? <Callout tone="danger">{error}</Callout> : null}
          {ok ? (
            <Callout tone="ok">
              Password changed. Other sessions were signed out.
            </Callout>
          ) : null}
          <Field label="Current password">
            <Input
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              required
            />
          </Field>
          <Field
            label="New password"
            hint="At least 12 characters. There is no reset flow."
          >
            <Input
              type="password"
              autoComplete="new-password"
              minLength={12}
              value={next}
              onChange={(e) => setNext(e.target.value)}
              required
            />
          </Field>
          <Field label="Confirm new password">
            <Input
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              required
            />
          </Field>
          <Row>
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? "Changing…" : "Change password"}
            </Button>
          </Row>
        </Stack>
      </form>
    </Card>
  );
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
            Credential changes require the administrator password within the
            last five minutes.
          </Hint>
          {error ? <Callout tone="danger">{error}</Callout> : null}
          {ok ? (
            <Callout tone="ok">
              Reauthenticated. Mutations are allowed for five minutes.
            </Callout>
          ) : null}
          <Field label="Password">
            <Input
              id={reauthFieldID}
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
          <Field
            label="ID"
            hint="Lowercase letters, digits, hyphen, underscore."
          >
            <Input
              mono
              value={id}
              onChange={(e) => setId(e.target.value)}
              required
            />
          </Field>
          <Field label="Provider">
            <Input
              mono
              value={provider}
              onChange={(e) => setProvider(e.target.value)}
              required
            />
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
  const [providers, setProviders] = useState<OAuthProvider[]>([]);

  useEffect(() => {
    let cancelled = false;
    api.get<OAuthProvider[]>("/api/admin/credentials/oauth/providers").then(
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

  return (
    <Card title="OAuth">
      <Stack>
        <Hint>
          Starting a sign-in needs your administrator password from the last
          five minutes; finishing one does not, because the login itself can
          take longer than that. Tokens are never displayed, and the state is
          bound to this session and spent on first use.
        </Hint>
        {providers.map((p) => (
          <OAuthProviderBlock key={p.name} provider={p} onChanged={onChanged} />
        ))}
      </Stack>
    </Card>
  );
}

/**
 * One provider's login. A served flow redirects the browser and comes back to this
 * server. A pinned flow cannot: the provider registered a fixed loopback address that
 * belongs to a local command-line tool, so the browser lands on a page that fails to load
 * and the administrator pastes that address back here. Everything the served callback
 * verifies — the state, its session binding, the PKCE verifier, single use — is still
 * verified on that paste.
 */
function OAuthProviderBlock({
  provider,
  onChanged,
}: {
  provider: OAuthProvider;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needReauth, setNeedReauth] = useState(false);
  const [authUrl, setAuthUrl] = useState<string | null>(null);
  const [callbackUrl, setCallbackUrl] = useState("");
  const [done, setDone] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    setNeedReauth(false);
    try {
      await fn();
    } catch (err) {
      if (isReauth(err)) setNeedReauth(true);
      else setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  const begin = () =>
    act(async () => {
      const res = await api.post<{ authUrl: string }>(
        `/api/admin/credentials/oauth/${encodeURIComponent(provider.name)}/begin`,
      );
      onChanged();
      const authURL = checkedAuthURL(res.authUrl);
      if (!provider.manual) {
        window.location.assign(authURL);
        return;
      }
      setDone(null);
      setAuthUrl(authURL);
      window.open(authURL, "_blank", "noopener,noreferrer");
    });

  const complete = () =>
    act(async () => {
      const cred = await api.post<Credential>(
        `/api/admin/credentials/oauth/${encodeURIComponent(provider.name)}/manual`,
        { callbackUrl },
      );
      setCallbackUrl("");
      setAuthUrl(null);
      setDone(`Saved ${cred.id}.`);
      onChanged();
    });

  return (
    <Card muted title={provider.name}>
      <Stack>
        <Hint>
          Scopes:{" "}
          {provider.scopes.length > 0
            ? provider.scopes.join(" ")
            : "provider default"}
          .
        </Hint>
        {needReauth ? <ReauthNotice /> : null}
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {done ? <Callout tone="ok">{done}</Callout> : null}
        <Row>
          <Button type="button" disabled={busy} onClick={() => void begin()}>
            {provider.manual ? "Open sign-in page" : `Connect ${provider.name}`}
          </Button>
          {provider.importable ? (
            <Button
              type="button"
              disabled={busy}
              onClick={() => setImporting((v) => !v)}
            >
              {importing ? "Cancel import" : "Paste existing tokens"}
            </Button>
          ) : null}
        </Row>
        {provider.manual ? (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void complete();
            }}
          >
            <Stack>
              <Callout>
                Your browser finishes at <code>{provider.redirectUri}</code>. If
                this server is what answers there, you land back here signed in
                and there is nothing to paste. Otherwise the page will not load
                — that address is a port on your own machine, not this server —
                and the failed page is the point: copy the whole address out of
                the address bar and paste it below.
              </Callout>
              {authUrl ? (
                <Hint>
                  If no tab opened,{" "}
                  <a href={authUrl} target="_blank" rel="noreferrer">
                    open the sign-in page
                  </a>
                  .
                </Hint>
              ) : null}
              <Field
                label="Address your browser landed on"
                hint="Carries code and state. It works exactly once, within ten minutes of starting."
              >
                <Textarea
                  mono
                  value={callbackUrl}
                  onChange={(e) => setCallbackUrl(e.target.value)}
                  placeholder={`${provider.redirectUri}?code=...&state=...`}
                  required
                />
              </Field>
              <Row>
                <Button type="submit" variant="primary" disabled={busy}>
                  {busy ? "Finishing…" : "Finish sign-in"}
                </Button>
              </Row>
            </Stack>
          </form>
        ) : null}
        {importing ? (
          <ImportTokensForm
            provider={provider.name}
            busy={busy}
            onSubmit={(body) =>
              act(async () => {
                const cred = await api.post<Credential>(
                  `/api/admin/credentials/oauth/${encodeURIComponent(provider.name)}/import`,
                  body,
                );
                setImporting(false);
                setDone(`Imported ${cred.id}.`);
                onChanged();
              })
            }
          />
        ) : null}
      </Stack>
    </Card>
  );
}

type ImportBody = {
  accessToken: string;
  refreshToken: string;
  idToken: string;
  expiresIn: number;
};

/**
 * Adopts tokens another client already minted, such as a local `codex login`. The refresh
 * token is mandatory: without it the credential works until the access token expires and
 * then dies with no way back.
 */
function ImportTokensForm({
  provider,
  busy,
  onSubmit,
}: {
  provider: string;
  busy: boolean;
  onSubmit: (body: ImportBody) => Promise<void>;
}) {
  const [accessToken, setAccessToken] = useState("");
  const [refreshToken, setRefreshToken] = useState("");
  const [idToken, setIdToken] = useState("");

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void onSubmit({ accessToken, refreshToken, idToken, expiresIn: 0 });
      }}
    >
      <Stack>
        <Hint>
          Paste the tokens a local sign-in already produced for {provider}. Both
          clients then share one refresh token, and whichever renews first may
          invalidate the other — sign in above instead if you would rather not
          have that.
        </Hint>
        <Field label="Access token">
          <Textarea
            mono
            value={accessToken}
            onChange={(e) => setAccessToken(e.target.value)}
            required
          />
        </Field>
        <Field
          label="Refresh token"
          hint="Required. Without it the credential cannot renew itself."
        >
          <Textarea
            mono
            value={refreshToken}
            onChange={(e) => setRefreshToken(e.target.value)}
            required
          />
        </Field>
        <Field
          label="ID token"
          hint="Optional. Names the account the plan belongs to."
        >
          <Textarea
            mono
            value={idToken}
            onChange={(e) => setIdToken(e.target.value)}
          />
        </Field>
        <Row>
          <Button type="submit" variant="primary" disabled={busy}>
            Import
          </Button>
        </Row>
      </Stack>
    </form>
  );
}

function CredentialCard({
  cred,
  onChanged,
}: {
  cred: Credential;
  onChanged: () => void;
}) {
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
    api
      .get<string[]>(
        `/api/admin/credentials/${encodeURIComponent(cred.id)}/references`,
      )
      .then(
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
          <Badge tone={cred.status === "ok" ? "ok" : "danger"}>
            {cred.status}
          </Badge>
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
          <Hint>
            Referenced by {refs.join(", ")}. Remove those before deleting.
          </Hint>
        ) : null}
        {needReauth ? <ReauthNotice /> : null}
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {cred.kind === "api_key" ? (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void act(() =>
                api.put(
                  `/api/admin/credentials/${encodeURIComponent(cred.id)}`,
                  { secret },
                ),
              );
            }}
          >
            <Stack>
              <Field
                label="Replacement secret"
                hint="Never displayed after save."
              >
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
                      api.post(
                        `/api/admin/credentials/${encodeURIComponent(cred.id)}/rotate`,
                        { secret },
                      ),
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
            title={
              referenced
                ? "Remove the references above before deleting."
                : undefined
            }
            onClick={() =>
              void act(() =>
                api.del(
                  `/api/admin/credentials/${encodeURIComponent(cred.id)}`,
                ),
              )
            }
          >
            Delete
          </Button>
        </Row>
      </Stack>
    </Card>
  );
}
