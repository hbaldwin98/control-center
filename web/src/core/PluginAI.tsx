/**
 * The operator-facing AI setup for one plugin: what it will ask for, whether those
 * routes exist, and a picker that creates them. Lives on the plugin list and detail
 * screens so enabling BIDRL (or anything else) does not require a trip through Models
 * just to learn the names.
 */
import { useCallback } from "react";
import { Badge, Card, Hint, Stack, api, useSnapshot } from "@cc/ui";
import { ConnectProvider, NeedAssign, modelsReady, needLabel, needTone, type Provider } from "./aiSetup";
import type { ModelNeed, PluginState } from "./types";

type Credential = { id: string; kind: "api_key" | "oauth"; provider: string };

export function PluginAI({
  state,
  onChanged,
}: {
  state: PluginState;
  onChanged: () => void;
}) {
  const needs = state.models ?? [];
  const providers = useSnapshot<Provider[]>(
    useCallback((signal) => api.snapshot<Provider[]>("/api/admin/ai/providers", { signal }), []),
  );
  const creds = useSnapshot<Credential[]>(
    useCallback((signal) => api.snapshot<Credential[]>("/api/admin/credentials", { signal }), []),
    { events: "core.credential.**" },
  );

  if (needs.length === 0) return null;

  const list = providers.status === "ready" ? providers.data : [];
  const credentialList = creds.status === "ready" ? creds.data : [];
  const ready = modelsReady(needs);
  const reload = () => {
    providers.reload();
    onChanged();
  };

  return (
    <Card
      title="AI"
      actions={<Badge tone={ready ? "ok" : "warn"}>{ready ? "ready" : "needs setup"}</Badge>}
    >
      <div id="ai">
        <Stack>
          <Hint>
            This plugin calls models by name. Connect a provider once, then pick a model for
            each line. Fallbacks and prices can be edited later under Models.
          </Hint>
          {list.length === 0 ? (
            <ConnectProvider credentials={credentialList} onConnected={reload} />
          ) : null}
          {needs.map((need) => (
            <NeedBlock key={need.name} need={need} providers={list} onAssigned={reload} />
          ))}
        </Stack>
      </div>
    </Card>
  );
}

function NeedBlock({
  need,
  providers,
  onAssigned,
}: {
  need: ModelNeed;
  providers: Provider[];
  onAssigned: () => void;
}) {
  return (
    <Card
      muted
      title={need.purpose}
      actions={
        <>
          <Badge tone={needTone(need.status)}>{needLabel(need.status)}</Badge>
          <code className="cc-hint">{need.name}</code>
        </>
      }
    >
      <Stack>
        <Hint>
          {need.capabilities.join(", ")}
          {need.model ? ` · ${need.provider}/${need.model}` : ""}
        </Hint>
        <NeedAssign need={need} providers={providers} onAssigned={onAssigned} />
      </Stack>
    </Card>
  );
}
