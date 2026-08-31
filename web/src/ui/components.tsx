/** The shared component library. The only thing plugins import for UI. */
import type {
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
} from "react";
import { formatDateTime, formatTime, formatUSD } from "./format";

function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(" ");
}

/* ---- layout ---- */

export function Page({ children }: { children: ReactNode }) {
  return <div className="cc-page">{children}</div>;
}

export function PageHeader({
  title,
  lede,
  actions,
}: {
  title: string;
  lede?: string | undefined;
  actions?: ReactNode | undefined;
}) {
  return (
    <header className="cc-page__header">
      <div className="cc-page__heading">
        <h1>{title}</h1>
        {lede ? <p className="cc-page__lede">{lede}</p> : null}
      </div>
      {actions ? <div className="cc-row">{actions}</div> : null}
    </header>
  );
}

export function Card({
  title,
  actions,
  muted,
  children,
}: {
  title?: string | undefined;
  actions?: ReactNode | undefined;
  muted?: boolean | undefined;
  children: ReactNode;
}) {
  return (
    <section className={cx("cc-card", muted && "cc-card--muted")}>
      {title || actions ? (
        <div className="cc-card__head">
          {title ? <h2 className="cc-card__title">{title}</h2> : <span />}
          {actions ? <div className="cc-row">{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}

export function Stack({ children }: { children: ReactNode }) {
  return <div className="cc-stack">{children}</div>;
}

export function Grid({ children }: { children: ReactNode }) {
  return <div className="cc-grid">{children}</div>;
}

export function Row({ children }: { children: ReactNode }) {
  return <div className="cc-row">{children}</div>;
}

/** A filter bar above a listing. Its controls wrap instead of overflowing. */
export function Toolbar({ children }: { children: ReactNode }) {
  return <div className="cc-toolbar">{children}</div>;
}

/* ---- typography ---- */

/** Muted secondary text. Use this rather than borrowing a field's hint class. */
export function Hint({ children }: { children: ReactNode }) {
  return <p className="cc-hint">{children}</p>;
}

/** A monospaced block for logs and error detail. Wraps rather than scrolling sideways. */
export function LogBlock({ children }: { children: ReactNode }) {
  return <pre className="cc-log">{children}</pre>;
}

/** An em dash standing in for a value the record does not have. */
export function Dash() {
  return <span className="cc-dash">—</span>;
}

/**
 * A timestamp. Shows the local date and time, or just the time when `timeOnly` is set
 * for a column that is almost always today. The exact instant is in the tooltip.
 */
export function Time({ iso, timeOnly }: { iso: string; timeOnly?: boolean }) {
  if (!iso) return <Dash />;
  return (
    <time dateTime={iso} title={iso}>
      {timeOnly ? formatTime(iso) : formatDateTime(iso)}
    </time>
  );
}

/** A micro-USD amount rendered as currency. */
export function Money({ microUsd }: { microUsd: number }) {
  return <>{formatUSD(microUsd)}</>;
}

/* ---- controls ---- */

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "default" | "primary" | "danger";
  size?: "md" | "sm";
  /** Renders as a selected toggle and reports `aria-pressed`. */
  pressed?: boolean;
};

export function Button({
  variant = "default",
  size = "md",
  pressed,
  className,
  ...rest
}: ButtonProps) {
  return (
    <button
      className={cx(
        "cc-button",
        variant !== "default" && `cc-button--${variant}`,
        size !== "md" && `cc-button--${size}`,
        pressed && "cc-button--pressed",
        className,
      )}
      aria-pressed={pressed}
      {...rest}
    />
  );
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string | undefined;
  children: ReactNode;
}) {
  return (
    <label className="cc-field">
      <span className="cc-field__label">{label}</span>
      {children}
      {hint ? <span className="cc-field__hint">{hint}</span> : null}
    </label>
  );
}

type InputProps = InputHTMLAttributes<HTMLInputElement> & { mono?: boolean };

export function Input({ mono, className, ...rest }: InputProps) {
  return <input className={cx("cc-input", mono && "cc-input--mono", className)} {...rest} />;
}

export function Select({ className, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select className={cx("cc-input", "cc-select", className)} {...rest}>
      {children}
    </select>
  );
}

export function Checkbox({
  label,
  hint,
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string | undefined }) {
  return (
    <label className="cc-check">
      <input type="checkbox" {...rest} />
      <span>
        {label}
        {hint ? <span className="cc-field__hint"> {hint}</span> : null}
      </span>
    </label>
  );
}

/* ---- feedback ---- */

export function Callout({
  tone = "neutral",
  children,
}: {
  tone?: "neutral" | "ok" | "warn" | "danger";
  children: ReactNode;
}) {
  return (
    <div
      className={cx("cc-callout", tone !== "neutral" && `cc-callout--${tone}`)}
      role={tone === "danger" ? "alert" : undefined}
    >
      {children}
    </div>
  );
}

export function Badge({
  tone = "neutral",
  children,
}: {
  tone?: "neutral" | "ok" | "warn" | "danger";
  children: ReactNode;
}) {
  return <span className={cx("cc-badge", tone !== "neutral" && `cc-badge--${tone}`)}>{children}</span>;
}

export function EmptyState({ children }: { children: ReactNode }) {
  return <div className="cc-empty">{children}</div>;
}

/** The one loading placeholder. Every screen waits the same way. */
export function Loading({ label = "Loading…" }: { label?: string | undefined }) {
  return (
    <div className="cc-empty cc-empty--loading" role="status">
      {label}
    </div>
  );
}

/* ---- async data ---- */

type Loadable<T> =
  | { status: "loading"; data: T | null; error: null }
  | { status: "ready"; data: T; error: null }
  | { status: "error"; data: T | null; error: Error };

/**
 * Renders one snapshot's three states consistently: a placeholder while loading, a
 * danger callout on failure, and an empty state when there is nothing to show. Screens
 * supply only the success case.
 */
export function Async<T>({
  state,
  loading,
  empty,
  isEmpty,
  children,
}: {
  state: Loadable<T>;
  loading?: string | undefined;
  empty?: ReactNode | undefined;
  isEmpty?: ((data: T) => boolean) | undefined;
  children: (data: T) => ReactNode;
}) {
  if (state.status === "error") return <Callout tone="danger">{state.error.message}</Callout>;
  if (state.status === "loading") return <Loading label={loading} />;
  if (empty !== undefined && (isEmpty ? isEmpty(state.data) : isEmptyValue(state.data))) {
    return <EmptyState>{empty}</EmptyState>;
  }
  return <>{children(state.data)}</>;
}

function isEmptyValue(data: unknown): boolean {
  return Array.isArray(data) && data.length === 0;
}

/* ---- tables ---- */

/**
 * A table and its horizontal scroll container. Wide content scrolls inside the card
 * rather than pushing the whole page sideways.
 */
export function Table({ head, children }: { head: ReactNode; children: ReactNode }) {
  return (
    <div className="cc-table-scroll">
      <table className="cc-table">
        <thead>
          <tr>{head}</tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

/** A header cell for a column of controls: reserves the space, names it for readers. */
export function ActionsHeader({ label = "Actions" }: { label?: string | undefined }) {
  return (
    <th className="cc-table__actions">
      <span className="cc-sr-only">{label}</span>
    </th>
  );
}
