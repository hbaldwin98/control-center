/**
 * Connecting a provider and assigning its models to the names plugins already
 * declared. The Models screen and each plugin's admin card share this so the
 * operator does not have to invent route names or copy capabilities by hand.
 */
import { useCallback, useState, type FormEvent } from "react";
import {
  ApiError,
  Badge,
  Button,
  Callout,
  Card,
  Field,
  Hint,
  Input,
  Row,
  Select,
  Stack,
  api,
  useSnapshot,
} from "@cc/ui";
import type { ModelNeed, PluginState } from "./types";

export type Billing = "metered" | "subscription";
export type ProviderKind = "openai_compatible" | "codex" | "fake";

export type Provider = {
  id: string;
  kind: ProviderKind;
  baseUrl: string;
  credentialId: string;
  billing: Billing;
};

type CatalogModel = {
  id: string;
  displayName: string;
  priced: boolean;
  inputMicroUsdPerMillion: number;
  outputMicroUsdPerMillion: number;
};

type Credential = { id: string; kind: "api_key" | "oauth"; provider: string };

const CODEX_BASE_URL = "https://chatgpt.com/backend-api/codex";

const PRESETS: {
  id: string;
  label: string;
  kind: ProviderKind;
  baseUrl: string;
  billing: Billing;
  hint: string;
}[] = [
  {
    id: "openrouter",
    label: "OpenRouter",
    kind: "openai_compatible",
    baseUrl: "https://openrouter.ai/api/v1",
    billing: "metered",
    hint: "One key, many models. Paste an OpenRouter API key.",
  },
  {
    id: "openai",
    label: "OpenAI",
    kind: "openai_compatible",
    baseUrl: "https://api.openai.com/v1",
    billing: "metered",
    hint: "Platform API key from platform.openai.com.",
  },
  {
    id: "chatgpt",
    label: "ChatGPT subscription",
    kind: "codex",
    baseUrl: CODEX_BASE_URL,
    billing: "subscription",
    hint: "Uses an OAuth login from Settings, billed to the ChatGPT plan.",
  },
  {
    id: "local",
    label: "Local server",
    kind: "openai_compatible",
    baseUrl: "http://127.0.0.1:11434/v1",
    billing: "metered",
    hint: "Ollama, vLLM, or any /v1 server on this machine. Plain http is allowed on loopback.",
  },
  {
    id: "fake",
    label: "Fake (no network)",
    kind: "fake",
    baseUrl: "",
    billing: "metered",
    hint: "In-process echo. Enough to exercise a plugin without paying anyone.",
  },
];

function formatErr(err: unknown): string {
  return err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err);
}

function isReauth(err: unknown): boolean {
  return err instanceof ApiError && err.code === "reauth_required";
}

export function needTone(status: ModelNeed["status"]): "ok" | "warn" | "danger" | "neutral" {
  if (status === "ready") return "ok";
  if (status === "missing") return "warn";
  return "danger";
}

export function needLabel(status: ModelNeed["status"]): string {
  switch (status) {
    case "ready":
      return "ready";
    case "missing":
      return "needs a model";
    case "unhealthy":
      return "unhealthy";
    case "capability_mismatch":
      return "wrong capabilities";
  }
}

/** One form that creates a credential (if needed) and a provider. */
export function ConnectProvider({
  credentials,
  onConnected,
}: {
  credentials: Credential[];
  onConnected: () => void;
}) {
  const [presetID, setPresetID] = useState(PRESETS[0]?.id ?? "openrouter");
  const preset = PRESETS.find((p) => p.id === presetID) ?? PRESETS[0];
  const [id, setId] = useState(preset?.id ?? "");
  const [baseUrl, setBaseUrl] = useState(preset?.baseUrl ?? "");
  const [credentialId, setCredentialId] = useState("");
  const [secret, setSecret] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const choosePreset = (next: string) => {
    const p = PRESETS.find((x) => x.id === next);
    setPresetID(next);
    if (p) {
      setId(p.id);
      setBaseUrl(p.baseUrl);
      setCredentialId("");
      setSecret("");
    }
  };

  const codex = preset?.kind === "codex";
  const usable = credentials.filter((c) => (codex ? c.kind === "oauth" : true));
  const creatingKey = !codex && credentialId === "" && secret.trim() !== "";

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!preset) return;
    setBusy(true);
    setError(null);
    try {
      let cred = credentialId;
      if (creatingKey) {
        if (password) {
          await api.post("/api/auth/reauth", { password });
        }
        const created = await api.post<{ id: string }>("/api/admin/credentials", {
          id: `${id}-key`,
          provider: id,
          secret,
        });
        cred = created.id;
      }
      if (!cred) {
        setError(codex ? "Sign in under Settings first, then pick that credential." : "Paste an API key or pick a credential.");
        setBusy(false);
        return;
      }
      await api.put(`/api/admin/ai/providers/${encodeURIComponent(id)}`, {
        id,
        kind: preset.kind,
        baseUrl: preset.kind === "codex" && baseUrl === "" ? CODEX_BASE_URL : baseUrl,
        credentialId: cred,
        billing: preset.kind === "codex" ? "subscription" : preset.billing,
      });
      setSecret("");
      setPassword("");
      onConnected();
    } catch (err) {
      if (isReauth(err)) {
        setError("Confirm your administrator password in the field above, then try again.");
      } else {
        setError(formatErr(err));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)}>
      <Stack>
        <Hint>
          A provider is where requests go. Plugins never see it — they ask for named routes,
          and you point those names at a model below.
        </Hint>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        <Field label="Kind" hint={preset?.hint}>
          <Select value={presetID} onChange={(e) => choosePreset(e.target.value)}>
            {PRESETS.map((p) => (
              <option key={p.id} value={p.id}>
                {p.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Provider ID" hint="Routes name this. Lowercase, digits, hyphen, underscore.">
          <Input mono value={id} onChange={(e) => setId(e.target.value)} required />
        </Field>
        {preset?.kind !== "fake" && preset?.kind !== "codex" ? (
          <Field label="Base URL">
            <Input mono value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} />
          </Field>
        ) : null}
        {codex ? (
          <Field label="OAuth credential" hint="Created under Settings → OAuth.">
            <Select
              mono
              value={credentialId}
              onChange={(e) => setCredentialId(e.target.value)}
              required={usable.length > 0}
            >
              <option value="">{usable.length === 0 ? "None yet — sign in under Settings" : "Select…"}</option>
              {usable.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.id}
                </option>
              ))}
            </Select>
          </Field>
        ) : (
          <>
            {usable.length > 0 ? (
              <Field label="Existing credential" hint="Leave on “new key” to paste one now.">
                <Select mono value={credentialId} onChange={(e) => setCredentialId(e.target.value)}>
                  <option value="">New API key…</option>
                  {usable.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.id} ({c.kind})
                    </option>
                  ))}
                </Select>
              </Field>
            ) : null}
            {credentialId === "" ? (
              <>
                <Field label="API key" hint="Saved as a credential. Never displayed again.">
                  <Input
                    type="password"
                    autoComplete="new-password"
                    value={secret}
                    onChange={(e) => setSecret(e.target.value)}
                    required
                  />
                </Field>
                <Field
                  label="Administrator password"
                  hint="Credential changes need this within the last five minutes."
                >
                  <Input
                    type="password"
                    autoComplete="current-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    required
                  />
                </Field>
              </>
            ) : null}
          </>
        )}
        <Row>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? "Connecting…" : "Connect"}
          </Button>
        </Row>
      </Stack>
    </form>
  );
}

/** Pick a provider model for one declared plugin need. */
export function NeedAssign({
  need,
  providers,
  onAssigned,
}: {
  need: ModelNeed;
  providers: Provider[];
  onAssigned: () => void;
}) {
  const [providerID, setProviderID] = useState(need.provider || "");
  const selected = providerID || providers[0]?.id || "";
  const [model, setModel] = useState(need.model || "");
  const [inPrice, setInPrice] = useState("");
  const [outPrice, setOutPrice] = useState("");
  const [showPrices, setShowPrices] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [catalog, setCatalog] = useState<CatalogModel[] | null>(null);
  const [catalogBusy, setCatalogBusy] = useState(false);

  const loadCatalog = async (id: string, refresh: boolean) => {
    if (!id) return;
    setCatalogBusy(true);
    try {
      const models = await api.get<CatalogModel[]>(
        `/api/admin/ai/providers/${encodeURIComponent(id)}/models${refresh ? "?refresh=1" : ""}`,
      );
      setCatalog(models);
    } catch (err) {
      setError(formatErr(err));
      setCatalog([]);
    } finally {
      setCatalogBusy(false);
    }
  };

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const body: Record<string, unknown> = { provider: selected, model: model.trim() };
      if (showPrices) {
        body.inputMicroUsdPerMillion = Math.round(Number(inPrice) * 1_000_000) || 0;
        body.outputMicroUsdPerMillion = Math.round(Number(outPrice) * 1_000_000) || 0;
      }
      await api.put(`/api/admin/ai/routes/${encodeURIComponent(need.name)}/assign`, body);
      onAssigned();
    } catch (err) {
      const message = formatErr(err);
      setError(message);
      if (message.includes("price") || message.includes("unknown model")) {
        setShowPrices(true);
      }
    } finally {
      setBusy(false);
    }
  };

  if (providers.length === 0) {
    return <Hint>Connect a provider first, then pick a model here.</Hint>;
  }

  const listID = `models-${need.name}-${selected}`;
  const provider = providers.find((p) => p.id === selected);

  return (
    <Stack>
      {error ? <Callout tone="danger">{error}</Callout> : null}
      {need.status === "unhealthy" && need.lastError ? (
        <Callout tone="danger">{need.lastError}</Callout>
      ) : null}
      {need.status === "capability_mismatch" ? (
        <Hint>
          A route named <code>{need.name}</code> exists but does not advertise{" "}
          {need.capabilities.join(", ")}. Picking a model recreates it with the right contract.
        </Hint>
      ) : null}
      <Row>
        <Field label="Provider">
          <Select
            mono
            value={selected}
            onChange={(e) => {
              setProviderID(e.target.value);
              setCatalog(null);
              setModel("");
            }}
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
            catalog
              ? `${catalog.length} from ${selected}`
              : "Load models to pick from what this provider serves."
          }
        >
          <Input
            mono
            list={listID}
            value={model}
            onChange={(e) => setModel(e.target.value)}
            required
          />
        </Field>
      </Row>
      <datalist id={listID}>
        {(catalog ?? []).map((m) => (
          <option key={m.id} value={m.id}>
            {m.displayName || m.id}
          </option>
        ))}
      </datalist>
      {showPrices && provider?.billing !== "subscription" ? (
        <Row>
          <Field label="Input $ / million tokens">
            <Input
              mono
              inputMode="decimal"
              value={inPrice}
              onChange={(e) => setInPrice(e.target.value)}
            />
          </Field>
          <Field label="Output $ / million tokens">
            <Input
              mono
              inputMode="decimal"
              value={outPrice}
              onChange={(e) => setOutPrice(e.target.value)}
            />
          </Field>
        </Row>
      ) : null}
      <Row>
        <Button
          type="button"
          disabled={!selected || catalogBusy}
          onClick={() => void loadCatalog(selected, catalog !== null)}
        >
          {catalogBusy ? "Asking…" : catalog ? "Refresh models" : "Load models"}
        </Button>
        <Button type="button" variant="primary" disabled={busy || !model.trim()} onClick={() => void submit()}>
          {busy ? "Saving…" : need.status === "ready" ? "Change model" : "Use this model"}
        </Button>
      </Row>
    </Stack>
  );
}

type GroupedNeed = {
  name: string;
  capabilities: string[];
  purpose: string;
  status: ModelNeed["status"];
  usedBy: string[];
  representative: ModelNeed;
};

function groupNeeds(plugins: PluginState[]): GroupedNeed[] {
  const byName = new Map<string, GroupedNeed>();
  for (const p of plugins) {
    for (const m of p.models ?? []) {
      const label = p.name || p.pluginId;
      const existing = byName.get(m.name);
      if (!existing) {
        byName.set(m.name, {
          name: m.name,
          capabilities: m.capabilities,
          purpose: m.purpose,
          status: m.status,
          usedBy: [label],
          representative: m,
        });
        continue;
      }
      existing.usedBy.push(label);
      if (m.status !== "ready" && existing.status === "ready") {
        existing.status = m.status;
        existing.representative = m;
      }
    }
  }
  return [...byName.values()];
}

export function modelsReady(needs: ModelNeed[] | undefined): boolean {
  if (!needs || needs.length === 0) return true;
  return needs.every((n) => n.status === "ready");
}

/** Every distinct logical name plugins have declared, for the Models screen. */
export function PluginNeedsPanel({
  providers,
  onChanged,
}: {
  providers: Provider[];
  onChanged: () => void;
}) {
  const plugins = useSnapshot<PluginState[]>(
    useCallback((signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }), []),
    { events: ["core.plugin.**", "core.ai.usage"] },
  );
  if (plugins.status === "loading") return <Hint>Loading plugin needs…</Hint>;
  if (plugins.status === "error") return <Callout tone="danger">{plugins.error.message}</Callout>;
  const grouped = groupNeeds(plugins.data);
  if (grouped.length === 0) {
    return <Hint>No plugin has declared an AI route yet.</Hint>;
  }
  return (
    <Stack>
      <Hint>
        Plugins ask for these names. Pick a model for each and the host creates the route with
        the capabilities that plugin declared — you do not type <code>cheap-vision</code> by
        hand.
      </Hint>
      {grouped.map((g) => (
        <Card
          key={g.name}
          muted
          title={g.purpose}
          actions={
            <>
              <Badge tone={needTone(g.status)}>{needLabel(g.status)}</Badge>
              <code className="cc-hint">{g.name}</code>
            </>
          }
        >
          <Stack>
            <Hint>
              {g.capabilities.join(", ")} · used by {g.usedBy.join(", ")}
              {g.representative.model ? ` · ${g.representative.provider}/${g.representative.model}` : ""}
            </Hint>
            <NeedAssign
              need={g.representative}
              providers={providers}
              onAssigned={() => {
                plugins.reload();
                onChanged();
              }}
            />
          </Stack>
        </Card>
      ))}
    </Stack>
  );
}
