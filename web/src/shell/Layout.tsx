/** The authenticated frame: navigation, and the outlet core and plugin routes render into. */
import { useCallback } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { Button, NavIcon, api, useSnapshot } from "@cc/ui";
import type { NavItem, PluginDescriptor, PluginModule } from "@cc/ui";
import { useAlertChime } from "../core/AlertSound";
import { useSession } from "./session";

const dashboardNav: NavItem = { path: "/", label: "Command", icon: "command" };
const commandNav: NavItem[] = [
  { path: "/jobs", label: "Jobs", icon: "jobs" },
  { path: "/inbox", label: "Inbox", icon: "inbox" },
];
const systemNav: NavItem[] = [
  { path: "/plugins", label: "Plugins", icon: "plugins" },
  { path: "/events", label: "Events", icon: "events" },
  { path: "/settings", label: "Settings", icon: "settings" },
];
const adminNav: NavItem[] = [
  { path: "/sessions", label: "Sessions", icon: "sessions" },
  { path: "/costs", label: "Costs", icon: "costs" },
  { path: "/models", label: "Models", icon: "models" },
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
      <span className={mobile ? "cc-mobile-nav__icon" : "cc-nav__icon"}>
        <NavIcon name={item.icon ?? "plugin"} />
      </span>
      <span className={mobile ? "cc-mobile-nav__label" : undefined}>{item.label}</span>
    </NavLink>
  );
}

function LivePluginLink({
  plugin,
  name,
  disabled,
}: {
  plugin: PluginModule;
  name: string;
  disabled: boolean;
}) {
  const entry = plugin.nav[0];
  const path = entry?.path ?? `/plugins/${plugin.id}`;
  return (
    <NavLink
      to={path}
      className={`cc-nav__live-link${disabled ? " cc-nav__live-link--off" : ""}`}
      title={disabled ? `${name} is disabled` : name}
      aria-disabled={disabled || undefined}
    >
      <span className={`cc-nav__live-dot${disabled ? " cc-nav__live-dot--off" : ""}`} />
      <span className="cc-nav__live-copy">
        <span>{name}</span>
        <small>{disabled ? "Disabled" : "Active"}</small>
      </span>
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
  const descriptorNames = new Map(descriptors.map((descriptor) => [descriptor.id, descriptor.name]));
  const pluginNav = plugins.flatMap((p) =>
    p.nav.map((n) => ({ ...n, pluginId: p.id })),
  );
  const primaryNav: ShellNavItem[] = [
    dashboardNav,
    ...pluginNav.filter((item) => item.topLevel),
    ...commandNav,
  ];
  const pluginSectionNav = pluginNav.filter((item) => !item.topLevel);
  const mobileNav: ShellNavItem[] = [
    ...primaryNav,
    ...systemNav,
    ...adminNav,
    ...pluginSectionNav,
  ];
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
          <span className="cc-nav__brandmark">
            <NavIcon name="command" size={18} />
          </span>
          <span>Control Center</span>
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

        <div className="cc-nav__group">
          <div className="cc-nav__section">Admin</div>
          {adminNav.map((item) => (
            <ShellNavLink key={item.path} item={item} />
          ))}
        </div>

        {pluginSectionNav.length > 0 ? (
          <>
            <div className="cc-nav__section">Plugin tools</div>
            {pluginSectionNav.map((item) => (
              <ShellNavLink
                key={`${item.pluginId}:${item.path}`}
                item={item}
                disabled={enabled.get(item.pluginId) === false}
              />
            ))}
          </>
        ) : null}

        <div className="cc-nav__live">
          <div className="cc-nav__section">Live plugins</div>
          {plugins.map((plugin) => (
            <LivePluginLink
              key={plugin.id}
              plugin={plugin}
              name={descriptorNames.get(plugin.id) ?? plugin.nav[0]?.label ?? plugin.id}
              disabled={enabled.get(plugin.id) === false}
            />
          ))}
        </div>

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
            <strong>{currentItem?.path === "/" ? "Overview" : currentItem?.label ?? "Overview"}</strong>
          </div>
          <div className="cc-topbar__actions">
            <NavLink className="cc-topbar__action" to="/inbox">
              <NavIcon name="inbox" />
              <span>Inbox</span>
            </NavLink>
            <NavLink className="cc-topbar__action" to="/events">
              <NavIcon name="bell" />
              <span>Events</span>
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
