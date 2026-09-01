/**
 * Operator hint when a plugin's declared AI routes are not ready. Plugin screens
 * import this from `@cc/ui` so they do not have to know how admin plugins are shaped.
 */
import { useCallback } from "react";
import { Callout } from "./components";
import { api } from "./api";
import { useSnapshot } from "./hooks";

type Need = { name: string; purpose: string; status: string };
type PluginRow = { pluginId: string; models?: Need[] };

export function PluginAIHint({ pluginId }: { pluginId: string }) {
  const snap = useSnapshot<PluginRow[]>(
    useCallback((signal) => api.snapshot<PluginRow[]>("/api/admin/plugins", { signal }), []),
    { events: ["core.plugin.**", "core.ai.usage"] },
  );
  if (snap.status !== "ready") return null;
  const me = snap.data.find((p) => p.pluginId === pluginId);
  const blocked = (me?.models ?? []).filter((m) => m.status !== "ready");
  if (blocked.length === 0) return null;
  const names = blocked.map((m) => m.purpose.replace(/\.$/, "")).join("; ");
  return (
    <Callout tone="warn">
      AI is not set up yet: {names}. Pick a model on the{" "}
      <a href={`/plugins/${encodeURIComponent(pluginId)}/settings#ai`}>plugin screen</a>.
    </Callout>
  );
}
