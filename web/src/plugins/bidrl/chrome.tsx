/** Navigation and layout shared across screens. */
import {
  type ReactNode,
} from "react";
import {
  Button,
  EmptyState,
  Link,
  Metric,
  Tabs,
  usePath,
} from "@cc/ui";
import {
  type LocationGroup,
} from "./model";
import { remembered } from "./place";

/**
 * Grid and table are two views of one list, not two actions, so they sit in a single
 * joined control where the pressed half reads as the current view.
 */
export function ViewToggle({ value, onChange }: { value: "grid" | "table"; onChange: (view: "grid" | "table") => void }) {
  return (
    <div className="bidrl-seg" role="group" aria-label="Lot view">
      <Button size="sm" pressed={value === "grid"} onClick={() => onChange("grid")}>
        Grid
      </Button>
      <Button size="sm" pressed={value === "table"} onClick={() => onChange("table")}>
        Table
      </Button>
    </div>
  );
}

export function BidrlLink({
  href,
  children,
  ariaLabel,
}: {
  href: string;
  children?: string;
  ariaLabel?: string;
}) {
  if (!href) return null;
  return (
    <a href={href} target="_blank" rel="noreferrer" aria-label={ariaLabel}>
      {children ?? "Open on BidRL"}
    </a>
  );
}

export function LocationSections<T>({
  groups,
  empty,
  children,
}: {
  groups: LocationGroup<T>[];
  empty: string;
  children: (items: T[]) => ReactNode;
}) {
  if (groups.length === 0) {
    return <EmptyState>{empty}</EmptyState>;
  }
  return (
    <div className="bidrl-locations">
      {groups.map((group, i) => (
        <details key={group.key} className="bidrl-location" open={i === 0 || groups.length <= 3}>
          <summary>
            <span className="bidrl-location__name">{group.label}</span>
            <span className="bidrl-location__meta">
              {group.items.length} auction{group.items.length === 1 ? "" : "s"}
            </span>
          </summary>
          <div className="bidrl-location__body">{children(group.items)}</div>
        </details>
      ))}
    </div>
  );
}

/**
 * The four sections, and which one a detail screen belongs to: a lot page is still the
 * catalog, an auction page is still Auctions, so the tab strip never goes blank under a
 * record you drilled into.
 */
const BIDRL_TABS = [
  { to: "/bidrl", label: "Overview", owns: (path: string) => path === "/bidrl" },
  {
    to: "/bidrl/auctions",
    label: "Auctions",
    owns: (path: string) => path === "/bidrl/auctions" || path.startsWith("/bidrl/auction/"),
  },
  {
    to: "/bidrl/lots",
    label: "Lots",
    owns: (path: string) => path === "/bidrl/lots" || path.startsWith("/bidrl/lot/"),
  },
  {
    to: "/bidrl/findings",
    label: "Findings",
    owns: (path: string) => path === "/bidrl/findings" || path === "/bidrl/watchlists",
  },
  { to: "/bidrl/saved", label: "Saved", owns: (path: string) => path === "/bidrl/saved" },
  { to: "/bidrl/intent", label: "Intent", owns: (path: string) => path === "/bidrl/intent" },
  {
    to: "/bidrl/automation",
    label: "Automation",
    owns: (path: string) => path === "/bidrl/automation",
  },
] as const;

export function BidrlTabs() {
  const path = usePath().replace(/\/+$/, "") || "/";
  return (
    <Tabs label="BIDRL sections">
      {BIDRL_TABS.map((item) => (
        <Link
          key={item.to}
          to={remembered(item.to)}
          aria-current={item.owns(path) ? "page" : undefined}
        >
          {item.label}
        </Link>
      ))}
    </Tabs>
  );
}

/** A metric that is also the way in to the list it counts. */
export function StatLink({
  to,
  label,
  value,
  hint,
  tone,
}: {
  to: string;
  label: string;
  value: string;
  hint?: string | undefined;
  tone?: "neutral" | "ok" | "warn" | "danger" | undefined;
}) {
  return (
    <Link to={to} className="bidrl-stat">
      <Metric label={label} value={value} hint={hint} tone={tone ?? "neutral"} />
    </Link>
  );
}
