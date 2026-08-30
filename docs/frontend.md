# Frontend

A single React/TypeScript SPA. The shell provides routing, layout, navigation, auth, the
live data connection, and a shared component library. Plugins provide routes and
components.

---

## Layout

```
web/src/
  shell/            layout, nav, auth, router, SSE client
  ui/               shared design system — the ONLY thing plugins import
  core/             dashboard, jobs, events, costs, settings, plugin admin
  plugins/
    hello/index.tsx
    bidrl/index.tsx
  plugins.ts        ← the only file importing plugin modules
```

This mirrors the backend deliberately: one registration file, plugins isolated behind a
declared entry point.

---

## The plugin UI contract

One entry point per plugin, importing only `@cc/ui`:

```tsx
import type { PluginModule } from "@cc/ui";

const bidrl: PluginModule = {
  id: "bidrl",
  nav: [{ path: "/bidrl", label: "BIDRL", icon: "gavel" }],
  routes: [
    { path: "/bidrl", element: <Feed /> },
    { path: "/bidrl/auction/:id", element: <Auction /> },
  ],
};

export default bidrl;
```

Registered in one place:

```ts
// web/src/plugins.ts
import bidrl from "./plugins/bidrl";
import hello from "./plugins/hello";

export const plugins = [hello, bidrl];
```

### The rule that keeps this modular

> Plugin components may import from `@cc/ui` and their own directory. **Never** from
> `shell/` or `core/`.

For v1 these are compiled into the main bundle. The import surface is identical to what a
dynamically loaded ESM module would use, so switching to dynamic loading later is a build
change rather than a rewrite — but only if the rule above has been held.

---

## Live data

One SSE stream at `/api/stream`, multiplexed by event pattern. The client subscribes to
patterns; the server filters and fans out.

```tsx
const deals = useEvents("bidrl.deal_found");
const cost  = useEvents("core.ai.usage", { filter: e => e.plugin === "bidrl" });
```

Everything live in the UI rides this one connection — job progress, cost tickers, plugin
state changes, notifications. There is no second realtime mechanism and no polling.

`useEvents` is provided by `@cc/ui`, so a plugin gets live data without knowing how the
transport works.

---

## Core screens

| Screen | Shows |
|---|---|
| Dashboard | per-plugin state, spend today/hour, running jobs, recent alerts |
| Plugins | enable/disable toggles, budgets, health, last error |
| Jobs | queue, history, per-job progress and logs, cancel |
| Events | live event log, filterable by pattern |
| Costs | spend by plugin → job → logical model, over time |
| Settings | credentials (with re-auth), model routing, notification rules, channels |

The Plugins screen is where the kill switch lives. Disabled plugins are greyed with the
reason and timestamp, never hidden.

For `Automated: true` plugins, the toggle is disabled until a daily budget is set, with the
reason shown inline — the UI half of the guardrail enforced in
[`policy`](modules/policy.md).

---

## Auth

Single user. Session cookie, `SameSite=Lax`, CSRF token on mutating requests. The app is
bound to a private network interface and is not exposed publicly, so there is no account
system, no OIDC, and no password reset flow.
