/** The authenticated frame: navigation, and the outlet core and plugin routes render into. */
import { useCallback } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { Button, api, useSnapshot } from "@cc/ui";
import type { PluginDescriptor, PluginModule } from "@cc/ui";
import { useSession } from "./session";

const coreNav = [
  { path: "/", label: "Dashboard" },
  { path: "/plugins", label: "Plugins" },
  { path: "/jobs", label: "Jobs" },
  { path: "/events", label: "Events" },
  { path: "/costs", label: "Costs" },
  { path: "/settings", label: "Settings" },
];

type PluginState = { pluginId: string; enabled: boolean };

export function Layout({
  plugins,
  descriptors,
}: {
  plugins: PluginModule[];
  descriptors: PluginDescriptor[];
}) {
  const { logout } = useSession();
  const live = useSnapshot<PluginState[]>(
    useCallback((signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }), []),
    { events: "core.plugin.**" },
  );
  const enabled = new Map(
    live.status === "ready"
      ? live.data.map((p) => [p.pluginId, p.enabled] as const)
      : descriptors.map((d) => [d.id, d.enabled] as const),
  );
  const pluginNav = plugins.flatMap((p) => p.nav.map((n) => ({ ...n, pluginId: p.id })));

  return (
    <div className="cc-shell">
      <nav className="cc-nav" aria-label="Primary">
        <div className="cc-nav__brand">
          Control Center <small>v1</small>
        </div>

        <div className="cc-nav__section">Core</div>
        {coreNav.map((item) => (
          <NavLink key={item.path} to={item.path} end={item.path === "/"} className="cc-nav__link">
            {item.label}
          </NavLink>
        ))}

        {pluginNav.length > 0 ? (
          <>
            <div className="cc-nav__section">Plugins</div>
            {pluginNav.map((item) => {
              const off = enabled.get(item.pluginId) === false;
              return (
                <NavLink
                  key={`${item.pluginId}:${item.path}`}
                  to={item.path}
                  className={off ? "cc-nav__link cc-nav__link--off" : "cc-nav__link"}
                  title={off ? `${item.label} is disabled` : undefined}
                >
                  {item.label}
                </NavLink>
              );
            })}
          </>
        ) : null}

        <div className="cc-nav__spacer" />
        <div className="cc-nav__footer">
          <Button size="sm" onClick={() => void logout()}>
            Sign out
          </Button>
        </div>
      </nav>

      <main className="cc-main">
        <Outlet />
      </main>
    </div>
  );
}
