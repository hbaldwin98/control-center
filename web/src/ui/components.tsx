/** The shared component library. The only thing plugins import for UI. */
import type {
    ButtonHTMLAttributes,
    CSSProperties,
    InputHTMLAttributes,
    ReactNode,
    SelectHTMLAttributes,
    TextareaHTMLAttributes,
} from "react";
import { useState } from "react";
import {
    formatDateTime,
    formatRelative,
    formatRemaining,
    formatTime,
    formatUSD,
    parseInstant,
} from "./format";
import { useNow } from "./hooks";

function cx(...parts: (string | false | null | undefined)[]): string {
    return parts.filter(Boolean).join(" ");
}

/* ---- layout ---- */

export function Page({ children }: { children: ReactNode }) {
    return <div className="cc-page">{children}</div>;
}

export function PageHeader({
    eyebrow,
    title,
    lede,
    actions,
}: {
    eyebrow?: ReactNode | undefined;
    title: string;
    lede?: string | undefined;
    actions?: ReactNode | undefined;
}) {
    return (
        <header className="cc-page__header">
            <div className="cc-page__heading">
                {eyebrow ? <span className="cc-page__eyebrow">{eyebrow}</span> : null}
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
    className,
    children,
}: {
    /** A node rather than a string, so a card can make its own title a link. */
    title?: ReactNode | undefined;
    actions?: ReactNode | undefined;
    muted?: boolean | undefined;
    className?: string | undefined;
    children: ReactNode;
}) {
    return (
        <section
            className={cx("cc-card", muted && "cc-card--muted", className)}
        >
            {title || actions ? (
                <div className="cc-card__head">
                    {title ? (
                        <h2 className="cc-card__title">{title}</h2>
                    ) : (
                        <span />
                    )}
                    {actions ? <div className="cc-row">{actions}</div> : null}
                </div>
            ) : null}
            {children}
        </section>
    );
}

/**
 * A collapsed section that opens on click. Long reference lists (event catalogs,
 * payload fields) live behind one of these so a screen shows what it is about
 * before it shows every detail it holds.
 */
export function Disclosure({
    summary,
    detail,
    open,
    defaultOpen,
    onToggle,
    children,
}: {
    summary: ReactNode;
    /** Shown beside the summary while closed, so the row is still informative. */
    detail?: ReactNode | undefined;
    /** Pass with `onToggle` to drive the section from outside, e.g. from a filter. */
    open?: boolean | undefined;
    defaultOpen?: boolean | undefined;
    onToggle?: ((open: boolean) => void) | undefined;
    children: ReactNode;
}) {
    const [uncontrolled, setUncontrolled] = useState(defaultOpen ?? false);
    const isOpen = open ?? uncontrolled;
    return (
        <div className="cc-disclosure">
            <button
                type="button"
                className="cc-disclosure__summary"
                aria-expanded={isOpen}
                onClick={() => {
                    if (open === undefined) setUncontrolled(!isOpen);
                    onToggle?.(!isOpen);
                }}
            >
                <span className="cc-disclosure__caret" aria-hidden="true">
                    {isOpen ? "▾" : "▸"}
                </span>
                <span className="cc-disclosure__label">{summary}</span>
                {detail ? (
                    <span className="cc-disclosure__detail">{detail}</span>
                ) : null}
            </button>
            {isOpen ? (
                <div className="cc-disclosure__body">{children}</div>
            ) : null}
        </div>
    );
}

export function Stack({ children }: { children: ReactNode }) {
    return <div className="cc-stack">{children}</div>;
}

/**
 * `density` names the column width rather than passing pixels per call: `metric` for a
 * strip of figures, `tile` for cards with a body. The widths live in the stylesheet.
 */
export function Grid({
    children,
    density,
}: {
    children: ReactNode;
    density?: "metric" | "tile" | undefined;
}) {
    return (
        <div className={cx("cc-grid", density && `cc-grid--${density}`)}>
            {children}
        </div>
    );
}

export function Row({ children }: { children: ReactNode }) {
    return <div className="cc-row">{children}</div>;
}

/** A filter bar above a listing. Its controls wrap instead of overflowing. */
export function Toolbar({ children }: { children: ReactNode }) {
    return <div className="cc-toolbar">{children}</div>;
}

/** In-page section links. Children are `Link` or `a` elements; the current one sets `aria-current="page"`. */
export function Tabs({
    label,
    children,
}: {
    label: string;
    children: ReactNode;
}) {
    return (
        <nav className="cc-tabs" aria-label={label}>
            {children}
        </nav>
    );
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

/** A micro-USD amount rendered as currency. `compact` caps it at cents, for a tile. */
export function Money({
    microUsd,
    compact,
}: {
    microUsd: number;
    compact?: boolean;
}) {
    return <>{formatUSD(microUsd, compact ? { compact } : {})}</>;
}

/**
 * Elapsed time that keeps counting: "12s ago", "4m ago". The exact instant is in the
 * tooltip, so recency is readable at a glance without losing the timestamp.
 */
export function RelativeTime({
    at,
    prefix,
}: {
    at: number | string | null;
    prefix?: string | undefined;
}) {
    const now = useNow();
    if (at === null) return <Dash />;
    const ms = typeof at === "number" ? at : parseInstant(at);
    if (!Number.isFinite(ms)) return <>{String(at)}</>;
    const iso = new Date(ms).toISOString();
    return (
        <time dateTime={iso} title={formatDateTime(iso)}>
            {prefix ? `${prefix} ` : ""}
            {formatRelative(ms, now)}
        </time>
    );
}

/** Time remaining until an instant. Ticks with `useNow`. */
export function Countdown({ iso }: { iso: string }) {
    const now = useNow();
    if (!iso) return <Dash />;
    const ms = parseInstant(iso);
    if (!Number.isFinite(ms)) return <>{iso}</>;
    const remaining = ms - now;
    const cls =
        remaining <= 0
            ? "cc-countdown cc-countdown--ended"
            : remaining < 60 * 60_000
              ? "cc-countdown cc-countdown--soon"
              : "cc-countdown";
    return (
        <time className={cls} dateTime={iso} title={formatDateTime(iso)}>
            {formatRemaining(ms, now)}
        </time>
    );
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
    return (
        <input
            className={cx("cc-input", mono && "cc-input--mono", className)}
            {...rest}
        />
    );
}

export function Select({
    mono,
    className,
    children,
    ...rest
}: SelectHTMLAttributes<HTMLSelectElement> & { mono?: boolean }) {
    return (
        <select
            className={cx(
                "cc-input",
                "cc-select",
                mono && "cc-input--mono",
                className,
            )}
            {...rest}
        >
            {children}
        </select>
    );
}

export function Textarea({
    mono,
    className,
    ...rest
}: TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean }) {
    return (
        <textarea
            className={cx(
                "cc-input",
                "cc-textarea",
                mono && "cc-input--mono",
                className,
            )}
            {...rest}
        />
    );
}

export function Checkbox({
    label,
    hint,
    ...rest
}: InputHTMLAttributes<HTMLInputElement> & {
    label: string;
    hint?: string | undefined;
}) {
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
            className={cx(
                "cc-callout",
                tone !== "neutral" && `cc-callout--${tone}`,
            )}
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
    return (
        <span
            className={cx(
                "cc-badge",
                tone !== "neutral" && `cc-badge--${tone}`,
            )}
        >
            {children}
        </span>
    );
}

export function EmptyState({ children }: { children: ReactNode }) {
    return <div className="cc-empty">{children}</div>;
}

/** The one loading placeholder. Every screen waits the same way. */
export function Loading({
    label = "Loading…",
}: {
    label?: string | undefined;
}) {
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
    if (state.status === "error")
        return <Callout tone="danger">{state.error.message}</Callout>;
    if (state.status === "loading") return <Loading label={loading} />;
    if (
        empty !== undefined &&
        (isEmpty ? isEmpty(state.data) : isEmptyValue(state.data))
    ) {
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
export function Table({
    head,
    className,
    children,
}: {
    head: ReactNode;
    /** For a screen that needs to shed columns on a narrow viewport. */
    className?: string | undefined;
    children: ReactNode;
}) {
    return (
        <div className="cc-table-scroll">
            <table className={cx("cc-table", className)}>
                <thead>
                    <tr>{head}</tr>
                </thead>
                <tbody>{children}</tbody>
            </table>
        </div>
    );
}

/** A header cell for a column of controls: reserves the space, names it for readers. */
export function ActionsHeader({
    label = "Actions",
}: {
    label?: string | undefined;
}) {
    return (
        <th className="cc-table__actions">
            <span className="cc-sr-only">{label}</span>
        </th>
    );
}

/**
 * A clickable column header. The table's current sort is `aria-sort` on the cell;
 * the control itself names the column and, when active, the direction.
 */
export function SortHeader({
    children,
    active,
    direction,
    onClick,
    numeric,
}: {
    children: string;
    active?: boolean | undefined;
    direction?: "asc" | "desc" | undefined;
    onClick: () => void;
    numeric?: boolean | undefined;
}) {
    const sorted = Boolean(active);
    const descending = direction === "desc";
    return (
        <th
            className={cx(numeric && "cc-num")}
            aria-sort={
                sorted ? (descending ? "descending" : "ascending") : "none"
            }
        >
            <button
                type="button"
                className="cc-table__sort"
                onClick={onClick}
                aria-label={
                    sorted
                        ? `Sort by ${children}, currently ${descending ? "descending" : "ascending"}`
                        : `Sort by ${children}`
                }
            >
                {children}
                <span className="cc-table__sort-indicator" aria-hidden="true">
                    {sorted && descending ? "▼" : "▲"}
                </span>
            </button>
        </th>
    );
}

/* ---- liveness ---- */

/**
 * A liveness indicator.
 *
 * `live` pulses, `idle` is a steady dot for a connection that is up but quiet, `stale`
 * warns that the stream is not delivering, and `off` is a hollow ring for a surface that
 * is not live at all. The label is what a screen reader and the tooltip say, so it has to
 * be a sentence a person can act on rather than a state name.
 */
export function LiveDot({
    state,
    label,
}: {
    state: "live" | "idle" | "stale" | "off";
    label: string;
}) {
    return (
        <span
            className={`cc-dot cc-dot--${state}`}
            role="img"
            aria-label={label}
            title={label}
        />
    );
}

/** A headline figure with its label, for the strip across the top of a screen. */
export function Metric({
    label,
    value,
    hint,
    tone = "neutral",
}: {
    label: string;
    value: ReactNode;
    hint?: ReactNode | undefined;
    tone?: "neutral" | "ok" | "warn" | "danger";
}) {
    return (
        <div
            className={cx(
                "cc-metric",
                tone !== "neutral" && `cc-metric--${tone}`,
            )}
        >
            <div className="cc-metric__label">{label}</div>
            <div className="cc-metric__value">{value}</div>
            {hint ? <div className="cc-metric__hint">{hint}</div> : null}
        </div>
    );
}

/**
 * How much of a limit is used.
 *
 * `soft` is the part that is only reserved, not yet committed: it is drawn hatched, so a
 * hold never reads as money already spent. With no `tone`, colour comes from the fraction
 * — filling the bar is what "danger" means for a budget. Pass one where it does not, as
 * for job progress, where being finished is good.
 *
 * The two widths are data, not design, so they ride CSS custom properties.
 */
export function Meter({
    value,
    soft = 0,
    max,
    label,
    tone,
}: {
    value: number;
    soft?: number;
    max: number;
    label?: string | undefined;
    tone?: "ok" | "warn" | "danger" | "accent" | undefined;
}) {
    if (max <= 0) return null;
    const used = Math.min(1, Math.max(0, value) / max);
    const held = Math.min(1 - used, Math.max(0, soft) / max);
    const fill = tone ?? (used >= 1 ? "danger" : used >= 0.8 ? "warn" : "ok");
    return (
        <div
            className="cc-meter"
            role="meter"
            aria-valuenow={Math.round(used * 100)}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-label={label ?? "usage"}
            style={
                {
                    "--meter-used": `${used * 100}%`,
                    "--meter-held": `${held * 100}%`,
                } as CSSProperties
            }
        >
            <span className={`cc-meter__fill cc-meter__fill--${fill}`} />
            <span className="cc-meter__held" />
        </div>
    );
}

/**
 * Event arrivals over time, oldest slice first.
 *
 * It shows shape and recency, not values, so it carries no axis and no numbers — the
 * figure beside it is the number.
 */
export function Sparkline({
    values,
    label,
    tall,
}: {
    values: number[];
    label: string;
    tall?: boolean;
}) {
    const height = tall ? 34 : 22;
    const peak = Math.max(1, ...values);
    const slot = 100 / Math.max(1, values.length);
    return (
        <svg
            className={cx("cc-spark", tall && "cc-spark--tall")}
            viewBox={`0 0 100 ${height}`}
            preserveAspectRatio="none"
            role="img"
            aria-label={label}
        >
            {values.map((v, i) => {
                const h =
                    v === 0 ? 1 : Math.max(1.5, (v / peak) * (height - 2));
                return (
                    <rect
                        key={i}
                        x={i * slot + slot * 0.15}
                        y={height - h}
                        width={slot * 0.7}
                        height={h}
                        rx={slot * 0.2}
                        className={cx(
                            "cc-spark__bar",
                            v === 0 && "cc-spark__bar--empty",
                        )}
                    />
                );
            })}
        </svg>
    );
}
