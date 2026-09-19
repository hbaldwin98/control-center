import type { ReactNode } from "react";
import type { NavIconName } from "./types";

type NavIconProps = {
  name: NavIconName;
  size?: number;
  className?: string;
};

/**
 * Every glyph the shell and plugin manifests may name, in display order.
 *
 * These are visual names, not plugin identities: a plugin picks the glyph that
 * reads to an operator, so a plugin can be renamed or removed without leaving
 * its name behind in the shared vocabulary. Exported as a runtime array so a
 * test can iterate the whole set rather than a hand-copied list that drifts.
 */
export const NAV_ICON_NAMES: NavIconName[] = [
  "command",
  "gavel",
  "jobs",
  "sessions",
  "inbox",
  "plugins",
  "events",
  "costs",
  "models",
  "settings",
  "plugin",
  "sparkle",
  "eye",
  "bolt",
  "bell",
  "search",
];

const GLYPHS: Record<NavIconName, ReactNode> = {
  command: (
    <>
      <rect x="3" y="3" width="5" height="5" rx="1" />
      <rect x="12" y="3" width="5" height="5" rx="1" />
      <rect x="3" y="12" width="5" height="5" rx="1" />
      <rect x="12" y="12" width="5" height="5" rx="1" />
    </>
  ),
  gavel: (
    <>
      <path d="m4 14 7-7" />
      <path d="m8 5 3-3 6 6-3 3" />
      <path d="M3 17h14" />
      <path d="m5 12 4 4" />
    </>
  ),
  jobs: (
    <>
      <rect x="3" y="4" width="14" height="13" rx="2" />
      <path d="M7 4V3h6v1M7 9h6M7 13h3" />
    </>
  ),
  sessions: (
    <>
      <rect x="3" y="4" width="14" height="10" rx="2" />
      <path d="M7 17h6M9 14v3M15 8h.01" />
    </>
  ),
  inbox: (
    <>
      <path d="M3 5h14l1 8a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2l1-8Z" />
      <path d="M3 11h3l1 2h4l1-2h3" />
    </>
  ),
  plugins: (
    <>
      <rect x="3" y="3" width="6" height="6" rx="1" />
      <rect x="11" y="3" width="6" height="6" rx="1" />
      <rect x="3" y="11" width="6" height="6" rx="1" />
      <rect x="11" y="11" width="6" height="6" rx="1" />
    </>
  ),
  events: (
    <>
      <path d="M3 12h3l2-7 3 11 2-7h4" />
      <path d="M3 4v13M17 4v13" />
    </>
  ),
  costs: (
    <>
      <circle cx="10" cy="10" r="7" />
      <path d="M10 6v8M13 8H9.25a1.5 1.5 0 0 0 0 3h1.5a1.5 1.5 0 0 1 0 3H7" />
    </>
  ),
  models: (
    <>
      <path d="m10 2 7 4v8l-7 4-7-4V6l7-4Z" />
      <path d="m3 6 7 4 7-4M10 10v8" />
    </>
  ),
  settings: (
    <>
      <circle cx="10" cy="10" r="3" />
      <path d="M10 2v2M10 16v2M2 10h2M16 10h2M4.3 4.3l1.4 1.4M14.3 14.3l1.4 1.4M15.7 4.3l-1.4 1.4M5.7 14.3l-1.4 1.4" />
    </>
  ),
  sparkle: (
    <>
      <path d="m10 2 1.2 5.8L17 10l-5.8 1.2L10 17l-1.2-5.8L3 10l5.8-2.2L10 2Z" />
      <path d="m16 14 .5 2.5L19 17l-2.5.5L16 20l-.5-2.5L13 17l2.5-.5L16 14Z" />
    </>
  ),
  eye: (
    <>
      <path d="M2.5 10s2.7-5 7.5-5 7.5 5 7.5 5-2.7 5-7.5 5-7.5-5-7.5-5Z" />
      <circle cx="10" cy="10" r="2" />
    </>
  ),
  bolt: <path d="m11 2-6 9h4l-1 7 6-9h-4l1-7Z" />,
  bell: <path d="M5 8a5 5 0 0 1 10 0c0 5 2 5 2 6H3c0-1 2-1 2-6ZM8 17h4" />,
  search: (
    <>
      <circle cx="8.5" cy="8.5" r="5" />
      <path d="m12.5 12.5 4.5 4.5" />
    </>
  ),
  plugin: (
    <>
      <path d="M7 3v4M13 3v4M5 7h10v4a3 3 0 0 1-3 3H8a3 3 0 0 1-3-3V7ZM10 14v3M7 17h6" />
    </>
  ),
};

/** Small, dependency-free line icons used by the shell and plugin manifests. */
export function NavIcon({ name, size = 16, className }: NavIconProps) {
  return (
    <svg
      aria-hidden="true"
      className={className}
      fill="none"
      focusable="false"
      height={size}
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="1.6"
      viewBox="0 0 20 20"
      width={size}
    >
      {GLYPHS[name] ?? <circle cx="10" cy="10" r="6" />}
    </svg>
  );
}
