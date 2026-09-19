import { useCallback, useState, type FormEvent } from "react";
import {
  ApiError,
  Async,
  Badge,
  Button,
  Callout,
  Card,
  Dash,
  EmptyState,
  Field,
  Hint,
  Input,
  Loading,
  Money,
  Page,
  PageHeader,
  Row,
  Select,
  Stack,
  Table,
  api,
  formatUSD,
  useSnapshot,
} from "@cc/ui";
import { ConnectProvider, PluginNeedsPanel } from "./aiSetup";

type Billing = "metered" | "subscription";
type ProviderKind = "openai_compatible" | "codex" | "fake";

type Provider = {
  id: string;
  kind: ProviderKind;
  baseUrl: string;
  credentialId: string;
  billing: Billing;
};

type Model = {
  provider: string;
  id: string;
  displayName: string;
  contextWindow: number;
  maxOutputTokens: number;
  inputMicroUsdPerMillion: number;
  outputMicroUsdPerMillion: number;
  priced: boolean;
  fetchedAt: string;
};

type Attempt = {
  provider: string;
  model: string;
  billing?: Billing;
  inputMicroUsdPerMillion: number;
  outputMicroUsdPerMillion: number;
};

type Route = {
  logicalName: string;
  capabilities: string[];
  maxInputTokens: number;
  maxOutputTokens: number;
  attemptPlan: Attempt[];
  healthy: boolean;
  lastError: string;
};

type Credential = { id: string; kind: "api_key" | "oauth"; provider: string };

const KINDS: { value: ProviderKind; label: string; hint: string }[] = [
  {
    value: "openai_compatible",
    label: "OpenAI-compatible",
    hint: "OpenAI, OpenRouter, or any server that serves /chat/completions with an API key.",
  },
  {
    value: "codex",
    label: "ChatGPT subscription (Codex)",
    hint: "The backend the Codex CLI talks to. Needs an OAuth credential and bills against the plan.",
  },
  {
    value: "fake",
    label: "Fake (local echo)",
    hint: "In process. Never contacts a network.",
  },
];

const CODEX_BASE_URL = "https://chatgpt.com/backend-api/codex";

/**
 * Providers, the model catalogs they publish, and the routes plugins ask for by name.
 *
 * Catalogs are read from the last fetch until someone asks for a refresh, so opening this
 * screen never calls out to every configured provider.
 */
export function Models() {
  const providers = useSnapshot<Provider[]>(
    useCallback(
      (signal) =>
        api.snapshot<Provider[]>("/api/admin/ai/providers", { signal }),
      [],
    ),
  );
  const routes = useSnapshot<Route[]>(
    useCallback(
      (signal) => api.snapshot<Route[]>("/api/admin/ai/routes", { signal }),
      [],
    ),
    { events: "core.ai.**" },
  );
  const creds = useSnapshot<Credential[]>(
    useCallback(
      (signal) =>
        api.snapshot<Credential[]>("/api/admin/credentials", { signal }),
      [],
    ),
    { events: "core.credential.**" },
  );
  const catalogs = useCatalogs();

  const providerList = providers.status === "ready" ? providers.data : [];
  const credentialList = creds.status === "ready" ? creds.data : [];
  const reloadAll = () => {
    providers.reload();
    routes.reload();
  };

  return (
    <Page>
      <PageHeader
        eyebrow="System / Models"
        title="Models"
        lede="Connect a provider, then pick a model for each thing a plugin needs. Fallbacks, prices, and extra routes live further down."
      />
      <Stack>
        <nav className="cc-jumpnav" aria-label="Model administration sections">
          <a href="#models-needs">Plugin needs</a>
          {providerList.length > 0 ? (
            <a href="#models-providers">Providers</a>
          ) : null}
          <a href="#models-routes">Routes</a>
        </nav>

        {providerList.length === 0 ? (
          <Card title="Connect a provider">
            <ConnectProvider
              credentials={credentialList}
              onConnected={reloadAll}
            />
          </Card>
        ) : null}

        <div id="models-needs" className="cc-group__title">
          What plugins need
        </div>
        <PluginNeedsPanel providers={providerList} onChanged={reloadAll} />

        {providerList.length > 0 ? (
          <>
            <div id="models-providers" className="cc-group__title">
              Providers
            </div>
            <Hint>
              A provider is a base URL, a credential, and how it charges.
              Plugins never see one: they name a route, and the route names
              these.
            </Hint>
            <Async
              state={providers}
              loading="Loading providers…"
              empty={<NoProviders />}
            >
              {(list) => (
                <Stack>
                  {list.map((p) => (
                    <ProviderCard
                      key={p.id}
                      provider={p}
                      credentials={credentialList}
                      catalog={catalogs.get(p.id)}
                      onLoadCatalog={(refresh) =>
                        void catalogs.load(p.id, refresh)
                      }
                      onChanged={reloadAll}
                    />
                  ))}
                  <Card title="Connect another provider">
                    <ConnectProvider
                      credentials={credentialList}
                      onConnected={reloadAll}
                    />
                  </Card>
                </Stack>
              )}
            </Async>
          </>
        ) : null}

        <div id="models-routes" className="cc-group__title">
          Advanced routes
        </div>
        <Hint>
          A route is the name a plugin asks for and the ordered attempts behind
          it. The prices here are the ones a call is admitted against;
          refreshing a catalog never reprices a route on its own.
        </Hint>
        <Async
          state={routes}
          loading="Loading routes…"
          empty="No routes yet. Add one below."
        >
          {(list) => (
            <Stack>
              {list.map((r) => (
                <RouteCard
                  key={r.logicalName}
                  route={r}
                  providers={providerList}
                  catalogs={catalogs}
                  onChanged={routes.reload}
                />
              ))}
            </Stack>
          )}
        </Async>
        <NewRouteCard
          providers={providerList}
          catalogs={catalogs}
          onChanged={routes.reload}
        />
      </Stack>
    </Page>
  );
}

function NoProviders() {
  return (
    <EmptyState>
      No providers yet. Add one below, then build a route on it.
    </EmptyState>
  );
}

/* ---- catalogs ---- */

type CatalogState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; models: Model[] }
  | { status: "error"; message: string };

type Catalogs = {
  get: (providerID: string) => CatalogState;
  load: (providerID: string, refresh: boolean) => Promise<void>;
};

/**
 * One cache of discovered models per provider, shared by every card and route editor on
 * the screen so a catalog is fetched once and reused.
 */
function useCatalogs(): Catalogs {
  const [state, setState] = useState<Record<string, CatalogState>>({});

  const load = useCallback(async (providerID: string, refresh: boolean) => {
    setState((s) => ({ ...s, [providerID]: { status: "loading" } }));
    try {
      const models = await api.get<Model[]>(
        `/api/admin/ai/providers/${encodeURIComponent(providerID)}/models${refresh ? "?refresh=1" : ""}`,
      );
      setState((s) => ({ ...s, [providerID]: { status: "ready", models } }));
    } catch (err) {
      setState((s) => ({
        ...s,
        [providerID]: { status: "error", message: formatErr(err) },
      }));
    }
  }, []);

  const get = useCallback(
    (providerID: string): CatalogState =>
      state[providerID] ?? { status: "idle" },
    [state],
  );

  return { get, load };
}

/* ---- providers ---- */

function ProviderCard({
  provider,
  credentials,
  catalog,
  onLoadCatalog,
  onChanged,
}: {
  provider: Provider;
  credentials: Credential[];
  catalog: CatalogState;
  onLoadCatalog: (refresh: boolean) => void;
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [showModels, setShowModels] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const remove = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.del(
        `/api/admin/ai/providers/${encodeURIComponent(provider.id)}`,
      );
      onChanged();
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  const reveal = () => {
    const next = !showModels;
    setShowModels(next);
    if (next && catalog.status === "idle") onLoadCatalog(false);
  };

  return (
    <Card
      title={provider.id}
      actions={
        <>
          <Badge>{kindLabel(provider.kind)}</Badge>
          <Badge
            tone={provider.billing === "subscription" ? "warn" : "neutral"}
          >
            {provider.billing}
          </Badge>
        </>
      }
    >
      <Stack>
        <Hint>
          {provider.baseUrl ? <code>{provider.baseUrl}</code> : "in process"} ·
          credential <code>{provider.credentialId}</code>
        </Hint>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        <Row>
          <Button type="button" pressed={showModels} onClick={reveal}>
            {showModels ? "Hide models" : "Show models"}
          </Button>
          <Button
            type="button"
            disabled={catalog.status === "loading"}
            onClick={() => onLoadCatalog(true)}
          >
            {catalog.status === "loading" ? "Asking…" : "Refresh from provider"}
          </Button>
          <Button
            type="button"
            pressed={editing}
            onClick={() => setEditing((v) => !v)}
          >
            {editing ? "Cancel" : "Edit"}
          </Button>
          <Button
            type="button"
            variant="danger"
            disabled={busy}
            onClick={() => void remove()}
          >
            {busy ? "Deleting…" : "Delete"}
          </Button>
        </Row>
        {editing ? (
          <ProviderForm
            existing={provider}
            credentials={credentials}
            onSaved={() => {
              setEditing(false);
              onChanged();
            }}
          />
        ) : null}
        {showModels ? (
          <CatalogTable catalog={catalog} billing={provider.billing} />
        ) : null}
      </Stack>
    </Card>
  );
}

function CatalogTable({
  catalog,
  billing,
}: {
  catalog: CatalogState;
  billing: Billing;
}) {
  if (catalog.status === "idle") return null;
  if (catalog.status === "loading")
    return <Loading label="Asking the provider…" />;
  if (catalog.status === "error")
    return <Callout tone="danger">{catalog.message}</Callout>;
  if (catalog.models.length === 0) {
    return <EmptyState>The provider returned no models.</EmptyState>;
  }
  return (
    <Table
      head={
        <>
          <th>Model</th>
          <th>Context</th>
          <th>Max output</th>
          <th>Input / M</th>
          <th>Output / M</th>
        </>
      }
    >
      {catalog.models.map((m) => (
        <tr key={m.id}>
          <td>
            <code>{m.id}</code>
            {m.displayName && m.displayName !== m.id ? (
              <Hint>{m.displayName}</Hint>
            ) : null}
          </td>
          <td>
            {m.contextWindow > 0 ? m.contextWindow.toLocaleString() : <Dash />}
          </td>
          <td>
            {m.maxOutputTokens > 0 ? (
              m.maxOutputTokens.toLocaleString()
            ) : (
              <Dash />
            )}
          </td>
          {m.priced ? (
            <>
              <td>
                <Money microUsd={m.inputMicroUsdPerMillion} />
              </td>
              <td>
                <Money microUsd={m.outputMicroUsdPerMillion} />
              </td>
            </>
          ) : (
            <td colSpan={2}>{priceless(billing)}</td>
          )}
        </tr>
      ))}
    </Table>
  );
}

// priceless says why a model has no price. A provider that publishes none is not free —
// it is unknown — unless the plan already paid for it.
function priceless(billing: Billing): string {
  return billing === "subscription"
    ? "billed to the plan"
    : "no price published";
}

function ProviderForm({
  existing,
  credentials,
  onSaved,
}: {
  existing?: Provider;
  credentials: Credential[];
  onSaved: () => void;
}) {
  const [id, setId] = useState(existing?.id ?? "");
  const [kind, setKind] = useState<ProviderKind>(
    existing?.kind ?? "openai_compatible",
  );
  const [baseUrl, setBaseUrl] = useState(existing?.baseUrl ?? "");
  const [credentialId, setCredentialId] = useState(
    existing?.credentialId ?? "",
  );
  const [billing, setBilling] = useState<Billing>(
    existing?.billing ?? "metered",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const codex = kind === "codex";
  const kindInfo = KINDS.find((k) => k.value === kind);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.put(`/api/admin/ai/providers/${encodeURIComponent(id)}`, {
        id,
        kind,
        baseUrl: codex && baseUrl === "" ? CODEX_BASE_URL : baseUrl,
        credentialId,
        billing: codex ? "subscription" : billing,
      });
      if (!existing) {
        setId("");
        setBaseUrl("");
        setCredentialId("");
      }
      onSaved();
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  const usable = credentials.filter((c) => !codex || c.kind === "oauth");

  return (
    <form onSubmit={submit}>
      <Stack>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        <Field
          label="ID"
          hint="Lowercase letters, digits, hyphen, underscore. Routes name this."
        >
          <Input
            mono
            value={id}
            onChange={(e) => setId(e.target.value)}
            disabled={Boolean(existing)}
            required
          />
        </Field>
        <Field label="Kind" hint={kindInfo?.hint}>
          <Select
            value={kind}
            onChange={(e) => setKind(e.target.value as ProviderKind)}
          >
            {KINDS.map((k) => (
              <option key={k.value} value={k.value}>
                {k.label}
              </option>
            ))}
          </Select>
        </Field>
        {kind !== "fake" ? (
          <Field
            label="Base URL"
            hint={
              codex
                ? `Defaults to ${CODEX_BASE_URL}. Change it only to front that backend with a proxy.`
                : "For example https://openrouter.ai/api/v1. Plain http is allowed only on loopback."
            }
          >
            <Input
              mono
              value={baseUrl}
              placeholder={codex ? CODEX_BASE_URL : "https://api.openai.com/v1"}
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </Field>
        ) : null}
        <Field
          label="Credential"
          hint={
            codex
              ? "Must be an OAuth login, not an API key."
              : "Created under Settings."
          }
        >
          <Select
            mono
            value={credentialId}
            onChange={(e) => setCredentialId(e.target.value)}
            required
          >
            <option value="">Select a credential…</option>
            {usable.map((c) => (
              <option key={c.id} value={c.id}>
                {c.id} ({c.kind})
              </option>
            ))}
          </Select>
        </Field>
        {usable.length === 0 ? (
          <Hint>
            {codex
              ? "No OAuth credential yet. Sign in to the provider under Settings first."
              : "No credentials yet. Create an API key under Settings first."}
          </Hint>
        ) : null}
        <Field
          label="Billing"
          hint={
            codex
              ? "Fixed: this backend authorizes with a plan, so a plan is what it charges."
              : "Metered attempts need a price and reserve their maximum before dispatch."
          }
        >
          <Select
            value={codex ? "subscription" : billing}
            disabled={codex}
            onChange={(e) => setBilling(e.target.value as Billing)}
          >
            <option value="metered">Metered (per token)</option>
            <option value="subscription">Subscription (already paid)</option>
          </Select>
        </Field>
        <Row>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? "Saving…" : existing ? "Save provider" : "Add provider"}
          </Button>
        </Row>
      </Stack>
    </form>
  );
}

/* ---- routes ---- */

function RouteCard({
  route,
  providers,
  catalogs,
  onChanged,
}: {
  route: Route;
  providers: Provider[];
  catalogs: Catalogs;
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const remove = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.del(
        `/api/admin/ai/routes/${encodeURIComponent(route.logicalName)}`,
      );
      onChanged();
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card
      title={<code>{route.logicalName}</code>}
      actions={
        <>
          {route.capabilities.map((c) => (
            <Badge key={c}>{c}</Badge>
          ))}
          <Badge tone={route.healthy ? "ok" : "danger"}>
            {route.healthy ? "healthy" : "unhealthy"}
          </Badge>
        </>
      }
    >
      <Stack>
        <Hint>
          {route.maxInputTokens.toLocaleString()} in /{" "}
          {route.maxOutputTokens.toLocaleString()} out ·{" "}
          {route.attemptPlan.length > 0 ? (
            <code>
              {route.attemptPlan
                .map((a) => `${a.provider}/${a.model}`)
                .join(" → ")}
            </code>
          ) : (
            "no attempts"
          )}
        </Hint>
        {!route.healthy && route.lastError ? (
          <Callout tone="danger">{route.lastError}</Callout>
        ) : null}
        {error ? <Callout tone="danger">{error}</Callout> : null}
        <Row>
          <Button
            type="button"
            pressed={editing}
            onClick={() => setEditing((v) => !v)}
          >
            {editing ? "Cancel" : "Edit"}
          </Button>
          <Button
            type="button"
            variant="danger"
            disabled={busy}
            onClick={() => void remove()}
          >
            {busy ? "Deleting…" : "Delete"}
          </Button>
        </Row>
        {editing ? (
          <RouteForm
            existing={route}
            providers={providers}
            catalogs={catalogs}
            onSaved={() => {
              setEditing(false);
              onChanged();
            }}
          />
        ) : null}
      </Stack>
    </Card>
  );
}

function NewRouteCard({
  providers,
  catalogs,
  onChanged,
}: {
  providers: Provider[];
  catalogs: Catalogs;
  onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  if (!open) {
    return (
      <Row>
        <Button
          type="button"
          onClick={() => setOpen(true)}
          disabled={providers.length === 0}
        >
          Add a route
        </Button>
        {providers.length === 0 ? <Hint>Add a provider first.</Hint> : null}
      </Row>
    );
  }
  return (
    <Card title="New route">
      <Stack>
        <RouteForm
          providers={providers}
          catalogs={catalogs}
          onSaved={() => {
            setOpen(false);
            onChanged();
          }}
        />
        <Row>
          <Button type="button" onClick={() => setOpen(false)}>
            Cancel
          </Button>
        </Row>
      </Stack>
    </Card>
  );
}

const emptyAttempt = (provider: string): Attempt => ({
  provider,
  model: "",
  inputMicroUsdPerMillion: 0,
  outputMicroUsdPerMillion: 0,
});

function RouteForm({
  existing,
  providers,
  catalogs,
  onSaved,
}: {
  existing?: Route;
  providers: Provider[];
  catalogs: Catalogs;
  onSaved: () => void;
}) {
  const [name, setName] = useState(existing?.logicalName ?? "");
  const [capabilities, setCapabilities] = useState(
    (existing?.capabilities ?? ["chat"]).join(", "),
  );
  const [maxInput, setMaxInput] = useState(
    String(existing?.maxInputTokens ?? 8000),
  );
  const [maxOutput, setMaxOutput] = useState(
    String(existing?.maxOutputTokens ?? 2000),
  );
  const [attempts, setAttempts] = useState<Attempt[]>(
    existing && existing.attemptPlan.length > 0
      ? existing.attemptPlan.map((a) => ({ ...a }))
      : [emptyAttempt(providers[0]?.id ?? "")],
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const byID = new Map(providers.map((p) => [p.id, p]));
  const patch = (i: number, next: Partial<Attempt>) =>
    setAttempts((list) =>
      list.map((a, j) => (j === i ? { ...a, ...next } : a)),
    );
  const move = (i: number, delta: number) =>
    setAttempts((list) => {
      const j = i + delta;
      const moved = list[i];
      if (!moved || j < 0 || j >= list.length) return list;
      const rest = list.filter((_, k) => k !== i);
      return [...rest.slice(0, j), moved, ...rest.slice(j)];
    });

  // The same arithmetic the host does before it dispatches: the most the whole plan
  // could cost, with a subscription attempt contributing nothing.
  const reserve = attempts.reduce((sum, a) => {
    if (byID.get(a.provider)?.billing === "subscription") return sum;
    const inCost = Math.ceil(
      ((Number(maxInput) || 0) * a.inputMicroUsdPerMillion) / 1_000_000,
    );
    const outCost = Math.ceil(
      ((Number(maxOutput) || 0) * a.outputMicroUsdPerMillion) / 1_000_000,
    );
    return sum + inCost + outCost;
  }, 0);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.put(`/api/admin/ai/routes/${encodeURIComponent(name)}`, {
        name,
        capabilities: capabilities
          .split(",")
          .map((c) => c.trim())
          .filter(Boolean),
        maxInputTokens: Number(maxInput) || 0,
        maxOutputTokens: Number(maxOutput) || 0,
        attempts: attempts.map((a) => ({
          provider: a.provider,
          model: a.model.trim(),
          inputMicroUsdPerMillion: a.inputMicroUsdPerMillion,
          outputMicroUsdPerMillion: a.outputMicroUsdPerMillion,
        })),
      });
      onSaved();
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit}>
      <Stack>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        <Field label="Name" hint="What a plugin passes as its logical model.">
          <Input
            mono
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={Boolean(existing)}
            required
          />
        </Field>
        <Field
          label="Capabilities"
          hint="Comma separated. A plugin's request must match one."
        >
          <Input
            value={capabilities}
            onChange={(e) => setCapabilities(e.target.value)}
            required
          />
        </Field>
        <Row>
          <Field
            label="Max input tokens"
            hint="Hard limit, and what a reservation assumes."
          >
            <Input
              mono
              inputMode="numeric"
              value={maxInput}
              onChange={(e) => setMaxInput(e.target.value)}
              required
            />
          </Field>
          <Field label="Max output tokens">
            <Input
              mono
              inputMode="numeric"
              value={maxOutput}
              onChange={(e) => setMaxOutput(e.target.value)}
              required
            />
          </Field>
        </Row>
        <Hint>
          Attempts run in order until one succeeds. Every call reserves{" "}
          <strong>{formatUSD(reserve)}</strong> up front — the most the whole
          plan could cost — and settles to what was actually used.
        </Hint>
        {attempts.map((a, i) => (
          <AttemptRow
            key={i}
            attempt={a}
            index={i}
            count={attempts.length}
            providers={providers}
            catalogs={catalogs}
            onPatch={(next) => patch(i, next)}
            onMove={(delta) => move(i, delta)}
            onRemove={() =>
              setAttempts((list) => list.filter((_, j) => j !== i))
            }
          />
        ))}
        <Row>
          <Button
            type="button"
            onClick={() =>
              setAttempts((list) => [
                ...list,
                emptyAttempt(providers[0]?.id ?? ""),
              ])
            }
          >
            Add attempt
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? "Saving…" : "Save route"}
          </Button>
        </Row>
      </Stack>
    </form>
  );
}

/**
 * One attempt. The model is a free text field backed by the provider's discovered
 * catalog: discovery makes the field easy to fill and prices it, but a model the catalog
 * has not heard of is still allowed, because a provider may serve more than it lists.
 */
function AttemptRow({
  attempt,
  index,
  count,
  providers,
  catalogs,
  onPatch,
  onMove,
  onRemove,
}: {
  attempt: Attempt;
  index: number;
  count: number;
  providers: Provider[];
  catalogs: Catalogs;
  onPatch: (next: Partial<Attempt>) => void;
  onMove: (delta: number) => void;
  onRemove: () => void;
}) {
  const provider = providers.find((p) => p.id === attempt.provider);
  const subscription = provider?.billing === "subscription";
  const catalog = catalogs.get(attempt.provider);
  const listID = `catalog-${attempt.provider}-${index}`;

  // Choosing a model the catalog priced fills the prices in, so the usual case is one
  // click. They stay editable: the price that governs a call is the one saved here.
  const chooseModel = (model: string) => {
    const known =
      catalog.status === "ready"
        ? catalog.models.find((m) => m.id === model)
        : undefined;
    if (known?.priced) {
      onPatch({
        model,
        inputMicroUsdPerMillion: known.inputMicroUsdPerMillion,
        outputMicroUsdPerMillion: known.outputMicroUsdPerMillion,
      });
      return;
    }
    onPatch({ model });
  };

  return (
    <Card muted title={`Attempt ${index + 1}`}>
      <Stack>
        <Row>
          <Field label="Provider">
            <Select
              mono
              value={attempt.provider}
              onChange={(e) => onPatch({ provider: e.target.value })}
            >
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.id}
                </option>
              ))}
            </Select>
          </Field>
          <Field
            label="Model"
            hint={
              catalog.status === "ready"
                ? `${catalog.models.length} discovered`
                : catalog.status === "error"
                  ? catalog.message
                  : "Load the catalog to pick from what the provider serves."
            }
          >
            <Input
              mono
              list={listID}
              value={attempt.model}
              onChange={(e) => chooseModel(e.target.value)}
              required
            />
          </Field>
        </Row>
        <datalist id={listID}>
          {catalog.status === "ready"
            ? catalog.models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.displayName || m.id}
                </option>
              ))
            : null}
        </datalist>
        <Row>
          <Button
            type="button"
            disabled={!attempt.provider || catalog.status === "loading"}
            onClick={() =>
              void catalogs.load(attempt.provider, catalog.status === "ready")
            }
          >
            {catalog.status === "loading"
              ? "Asking…"
              : catalog.status === "ready"
                ? "Refresh models"
                : "Load models"}
          </Button>
          <Button
            type="button"
            disabled={index === 0}
            onClick={() => onMove(-1)}
          >
            Move up
          </Button>
          <Button
            type="button"
            disabled={index === count - 1}
            onClick={() => onMove(1)}
          >
            Move down
          </Button>
          <Button
            type="button"
            variant="danger"
            disabled={count === 1}
            onClick={onRemove}
          >
            Remove
          </Button>
        </Row>
        {subscription ? (
          <Hint>
            {attempt.provider} bills against a subscription, so this attempt
            reserves nothing and needs no price. Its own rate limits are the
            ceiling.
          </Hint>
        ) : (
          <Row>
            <Field label="Input $ per million tokens">
              <Input
                mono
                inputMode="decimal"
                value={usdFromMicro(attempt.inputMicroUsdPerMillion)}
                onChange={(e) =>
                  onPatch({
                    inputMicroUsdPerMillion: microFromUsd(e.target.value),
                  })
                }
              />
            </Field>
            <Field label="Output $ per million tokens">
              <Input
                mono
                inputMode="decimal"
                value={usdFromMicro(attempt.outputMicroUsdPerMillion)}
                onChange={(e) =>
                  onPatch({
                    outputMicroUsdPerMillion: microFromUsd(e.target.value),
                  })
                }
              />
            </Field>
          </Row>
        )}
      </Stack>
    </Card>
  );
}

/* ---- helpers ---- */

function kindLabel(kind: ProviderKind): string {
  return KINDS.find((k) => k.value === kind)?.label ?? kind;
}

/** Micro-USD per million tokens is the unit everything below the UI uses. */
function usdFromMicro(micro: number): string {
  if (!micro) return "";
  return String(micro / 1_000_000);
}

function microFromUsd(text: string): number {
  const v = Number.parseFloat(text);
  if (!Number.isFinite(v) || v < 0) return 0;
  return Math.round(v * 1_000_000);
}

function formatErr(err: unknown): string {
  return err instanceof ApiError
    ? err.message
    : err instanceof Error
      ? err.message
      : String(err);
}
