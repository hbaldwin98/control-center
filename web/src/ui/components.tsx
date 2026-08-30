/** The shared component library. The only thing plugins import for UI. */
import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode } from "react";

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

export function Card({ title, muted, children }: { title?: string; muted?: boolean; children: ReactNode }) {
  return (
    <section className={muted ? "cc-card cc-card--muted" : "cc-card"}>
      {title ? <h2 className="cc-card__title">{title}</h2> : null}
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
