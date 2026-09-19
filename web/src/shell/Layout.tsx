/** The authenticated frame: navigation, and the outlet core and plugin routes render into. */
import { useCallback } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { Button, api, useSnapshot } from "@cc/ui";
import type { NavItem, PluginDescriptor, PluginModule } from "@cc/ui";
import { useAlertChime } from "../core/AlertSound";
import { useSession } from "./session";

const dashboardNav: NavItem = { path: "/", label: "Dashboard" };
const commandNav: NavItem[] = [
  dashboardNav,
  { path: "/jobs", label: "Jobs" },
  { path: "/sessions", label: "Sessions" },
  { path: "/inbox", label: "Inbox" },
];
const systemNav: NavItem[] = [
  { path: "/plugins", label: "Plugins" },
  { path: "/events", label: "Events" },
  { path: "/costs", label: "Costs" },
  { path: "/models", label: "Models" },
  { path: "/settings", label: "Settings" },
];

type PluginState = { pluginId: string; enabled: boolean };
type ShellNavItem = NavItem & { pluginId?: string };

function ShellNavLink({
  item,
  disabled,
  mobile = false,
}: {
  item: ShellNavItem;
  disabled?: boolean;
  mobile?: boolean;
}) {
  const className = mobile ? "cc-mobile-nav__link" : "cc-nav__link";
  const offClass = disabled
    ? mobile
      ? " cc-mobile-nav__link--off"
      : " cc-nav__link--off"
    : "";
  return (
    <NavLink
      to={item.path}
      end={item.path === "/"}
      className={`${className}${offClass}`}
      title={disabled ? `${item.label} is disabled` : undefined}
      aria-disabled={disabled || undefined}
    >
      <span>{item.label}</span>
    </NavLink>
  );
}

export function Layout({
  plugins,
  descriptors,
}: {
  plugins: PluginModule[];
  descriptors: PluginDescriptor[];
}) {
  const { logout } = useSession();
  const location = useLocation();
  // One listener for the whole authenticated session, so an alert rings once however
  // many screens are mounted.
  useAlertChime();
  const live = useSnapshot<PluginState[]>(
    useCallback(
      (signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }),
      [],
    ),
    { events: "core.plugin.**" },
  );
  const enabled = new Map(
    live.status === "ready"
      ? live.data.map((p) => [p.pluginId, p.enabled] as const)
      : descriptors.map((d) => [d.id, d.enabled] as const),
  );
  const pluginNav = plugins.flatMap((p) =>
    p.nav.map((n) => ({ ...n, pluginId: p.id })),
  );
  const primaryNav: ShellNavItem[] = [
    dashboardNav,
    ...pluginNav.filter((item) => item.topLevel),
    ...commandNav.slice(1),
  ];
  const pluginSectionNav = pluginNav.filter((item) => !item.topLevel);
  const mobileNav: ShellNavItem[] = [...primaryNav, ...systemNav, ...pluginSectionNav];
  const currentItem = mobileNav.find((item) =>
    item.path === "/"
      ? location.pathname === "/"
      : location.pathname === item.path || location.pathname.startsWith(`${item.path}/`),
  );

  return (
    <div className="cc-shell">
      <a className="cc-skip-link" href="#main-content">
        Skip to content
      </a>
      <nav className="cc-nav" aria-label="Primary">
        <div className="cc-nav__brand">
          Control Center <small>v2</small>
        </div>

        <div className="cc-nav__group">
          <div className="cc-nav__section">Command</div>
          {primaryNav.map((item) => (
            <ShellNavLink
              key={item.pluginId ? `${item.pluginId}:${item.path}` : item.path}
              item={item}
              disabled={item.pluginId ? enabled.get(item.pluginId) === false : false}
            />
          ))}
        </div>

        <div className="cc-nav__group">
          <div className="cc-nav__section">System</div>
          {systemNav.map((item) => (
            <ShellNavLink key={item.path} item={item} />
          ))}
        </div>

        {pluginSectionNav.length > 0 ? (
          <>
            <div className="cc-nav__section">Plugins</div>
            {pluginSectionNav.map((item) => (
              <ShellNavLink
                key={`${item.pluginId}:${item.path}`}
                item={item}
                disabled={enabled.get(item.pluginId) === false}
              />
            ))}
          </>
        ) : null}

        <div className="cc-nav__spacer" />
        <div className="cc-nav__footer">
          <Button size="sm" onClick={() => void logout()}>
            Sign out
          </Button>
        </div>
      </nav>

      <div className="cc-workspace">
        <header className="cc-topbar">
          <div className="cc-topbar__crumb">
            <span className="cc-topbar__context">Command surface</span>
            <span aria-hidden="true">/</span>
            <strong>{currentItem?.label ?? "Overview"}</strong>
          </div>
          <div className="cc-topbar__actions">
            <NavLink className="cc-topbar__action" to="/inbox">
              Inbox
            </NavLink>
            <NavLink className="cc-topbar__action" to="/events">
              Events
            </NavLink>
            <button
              className="cc-topbar__account"
              type="button"
              onClick={() => void logout()}
              aria-label="Sign out"
              title="Sign out"
            >
              W
            </button>
          </div>
        </header>
        <main id="main-content" className="cc-main" tabIndex={-1}>
          <Outlet />
        </main>
      </div>

      <nav className="cc-mobile-nav" aria-label="Mobile navigation">
        {mobileNav.map((item) => (
          <ShellNavLink
            key={item.pluginId ? `${item.pluginId}:${item.path}` : item.path}
            item={item}
            mobile
            disabled={item.pluginId ? enabled.get(item.pluginId) === false : false}
          />
        ))}
      </nav>
    </div>
  );
}
