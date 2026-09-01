/** The authenticated frame: navigation, and the outlet core and plugin routes render into. */
import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { Button } from "@cc/ui";
import type { PluginModule } from "@cc/ui";
import { useSession } from "./session";

const coreNav = [
  { path: "/", label: "Dashboard" },
  { path: "/plugins", label: "Plugins" },
  { path: "/jobs", label: "Jobs" },
  { path: "/events", label: "Events" },
  { path: "/costs", label: "Costs" },
  { path: "/settings", label: "Settings" },
];

export function Layout({ plugins }: { plugins: PluginModule[] }) {
  const { logout } = useSession();
  const pluginNav = plugins.flatMap((p) => p.nav.map((n) => ({ ...n, pluginId: p.id })));

  // On a narrow viewport the sidebar collapses behind this toggle. Navigating closes it;
  // at desktop widths the links are always visible and the toggle is hidden.
  const [open, setOpen] = useState(false);
  const { pathname } = useLocation();
  useEffect(() => setOpen(false), [pathname]);

  return (
    <div className="cc-shell">
      <nav className="cc-nav" data-open={open}>
        <div className="cc-nav__bar">
          <div className="cc-nav__brand">
            Control Center <small>v1</small>
          </div>
          <button
            type="button"
            className="cc-nav__toggle"
            aria-expanded={open}
            aria-controls="cc-nav-items"
            aria-label={open ? "Hide navigation" : "Show navigation"}
            onClick={() => setOpen((v) => !v)}
          >
            {open ? "Close" : "Menu"}
          </button>
        </div>

        <div className="cc-nav__items" id="cc-nav-items">
          {coreNav.map((item) => (
            <NavLink key={item.path} to={item.path} end={item.path === "/"} className="cc-nav__link">
              {item.label}
            </NavLink>
          ))}

          {pluginNav.length > 0 ? (
            <>
              <div className="cc-nav__section">Plugins</div>
              {pluginNav.map((item) => (
                <NavLink key={`${item.pluginId}:${item.path}`} to={item.path} className="cc-nav__link">
                  {item.label}
                </NavLink>
              ))}
            </>
          ) : null}

          <div className="cc-nav__spacer" />
          <Button onClick={() => void logout()}>Sign out</Button>
        </div>
      </nav>

      <main className="cc-main">
        <Outlet />
      </main>
    </div>
  );
}
