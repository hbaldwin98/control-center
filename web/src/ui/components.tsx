/** The shared component library. The only thing plugins import for UI. */
import type { ButtonHTMLAttributes, CSSProperties, InputHTMLAttributes, ReactNode } from "react";
import { useNow } from "./hooks";
import { formatRelative } from "./format";

export function PageHeader({
  title,
  lede,
  actions,
}: {
  title: string;
  lede?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="cc-page__header">
      <div>
        <h1>{title}</h1>
        {lede ? <p className="cc-page__lede">{lede}</p> : null}
      </div>
      {actions ? <div className="cc-row">{actions}</div> : null}
    </header>
  );
}

export function Page({ children }: { children: ReactNode }) {
  return <div className="cc-page">{children}</div>;
}

export function Card({
  title,
  actions,
  muted,
  className,
  children,
}: {
  title?: ReactNode;
  /** Rendered on the title row, right-aligned: badges, a live dot, a small control. */
  actions?: ReactNode;
  muted?: boolean;
  className?: string;
  children: ReactNode;
}) {
  const classes = ["cc-card"];
  if (muted) classes.push("cc-card--muted");
  if (className) classes.push(className);
  return (
    <section className={classes.join(" ")}>
      {title || actions ? (
        <div className="cc-card__head">
          {title ? <h2 className="cc-card__title">{title}</h2> : <span />}
          {actions ? <div className="cc-card__actions">{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}

export function Stack({ children }: { children: ReactNode }) {
  return <div className="cc-stack">{children}</div>;
}

export function Grid({ children, min }: { children: ReactNode; min?: number }) {
  const style = min
    ? ({ gridTemplateColumns: `repeat(auto-fill, minmax(min(${min}px, 100%), 1fr))` } as CSSProperties)
    : undefined;
  return (
    <div className="cc-grid" style={style}>
      {children}
    </div>
  );
}

export function Row({ children }: { children: ReactNode }) {
  return <div className="cc-row">{children}</div>;
}

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "default" | "primary" | "danger";
};

export function Button({ variant = "default", className, ...rest }: ButtonProps) {
  const classes = ["cc-button"];
  if (variant !== "default") classes.push(`cc-button--${variant}`);
  if (className) classes.push(className);
  return <button className={classes.join(" ")} {...rest} />;
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
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
  const classes = ["cc-input"];
  if (mono) classes.push("cc-input--mono");
  if (className) classes.push(className);
  return <input className={classes.join(" ")} {...rest} />;
}

export function Callout({
  tone = "neutral",
  children,
}: {
  tone?: "neutral" | "danger";
  children: ReactNode;
}) {
  return (
    <div className={tone === "danger" ? "cc-callout cc-callout--danger" : "cc-callout"} role={tone === "danger" ? "alert" : undefined}>
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
  const classes = ["cc-badge"];
  if (tone !== "neutral") classes.push(`cc-badge--${tone}`);
  return <span className={classes.join(" ")}>{children}</span>;
}

export function EmptyState({ children }: { children: ReactNode }) {
  return <div className="cc-empty">{children}</div>;
}

/**
 * A liveness indicator. `live` pulses, `idle` is a steady dot for a connection that is up
 * but quiet, `stale` warns, and `off` is a hollow ring for a surface that is not live at
 * all. The label is for screen readers and the tooltip.
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

/** A headline figure with its label, for the strip across the top of a dashboard. */
export function Metric({
  label,
  value,
  hint,
  tone = "neutral",
}: {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  tone?: "neutral" | "ok" | "warn" | "danger";
}) {
  const classes = ["cc-metric"];
  if (tone !== "neutral") classes.push(`cc-metric--${tone}`);
  return (
    <div className={classes.join(" ")}>
      <div className="cc-metric__label">{label}</div>
      <div className="cc-metric__value">{value}</div>
      {hint ? <div className="cc-metric__hint">{hint}</div> : null}
    </div>
  );
}

/**
 * A bar showing how much of a limit is used. `soft` is the part that is only reserved, not
 * yet committed, so a tile can show a hold without claiming it was spent.
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
  label?: string;
  /** Fixed colour. Omit for a limit, where filling the bar is what "danger" means. */
  tone?: "ok" | "warn" | "danger" | "accent";
}) {
  if (max <= 0) return null;
  const used = Math.min(1, value / max);
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
    >
      <span className={`cc-meter__fill cc-meter__fill--${fill}`} style={{ width: `${used * 100}%` }} />
      <span className="cc-meter__held" style={{ width: `${held * 100}%` }} />
    </div>
  );
}

/**
 * Event arrivals over time, oldest slice first. It shows shape and recency, not values,
 * so it carries no axis and no numbers — the figure beside it is the number.
 */
export function Sparkline({
  values,
  label,
  height = 22,
}: {
  values: number[];
  label: string;
  height?: number;
}) {
  const peak = Math.max(1, ...values);
  const count = Math.max(1, values.length);
  const slot = 100 / count;
  return (
    <svg
      className="cc-spark"
      viewBox={`0 0 100 ${height}`}
      preserveAspectRatio="none"
      height={height}
      role="img"
      aria-label={label}
    >
      {values.map((v, i) => {
        const h = v === 0 ? 1 : Math.max(1.5, (v / peak) * (height - 2));
        return (
          <rect
            key={i}
            x={i * slot + slot * 0.15}
            y={height - h}
            width={slot * 0.7}
            height={h}
            rx={slot * 0.2}
            className={v === 0 ? "cc-spark__bar cc-spark__bar--empty" : "cc-spark__bar"}
          />
        );
      })}
    </svg>
  );
}

/** A timestamp that keeps counting: "just now", "12s ago", "4m ago". */
export function RelativeTime({ at, prefix }: { at: number | string | null; prefix?: string }) {
  const now = useNow();
  if (at === null) return null;
  const ms = typeof at === "number" ? at : Date.parse(at);
  if (!Number.isFinite(ms)) return <>{String(at)}</>;
  return (
    <time dateTime={new Date(ms).toISOString()} title={new Date(ms).toLocaleString()}>
      {prefix ? `${prefix} ` : ""}
      {formatRelative(ms, now)}
    </time>
  );
}
