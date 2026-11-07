import { AlertTriangle, Check, Copy } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";

export type Tone = "neutral" | "ok" | "warn" | "bad" | "info";

export function ErrorNote({ error }: { error: unknown }) {
  if (!error) return null;
  const message = error instanceof Error ? error.message : String(error);
  return (
    <p className="error" role="alert">
      <AlertTriangle size={14} aria-hidden />
      <span>{message}</span>
    </p>
  );
}

export function Panel({
  title,
  count,
  actions,
  footer,
  flush = true,
  children,
}: {
  title: string;
  count?: number;
  actions?: ReactNode;
  footer?: ReactNode;
  /** Render children edge-to-edge (tables, lists) instead of padded. */
  flush?: boolean;
  children: ReactNode;
}) {
  return (
    <section className="panel">
      <header className="panel-header">
        <h2>
          {title}
          {count !== undefined && <span className="count">{count}</span>}
        </h2>
        {actions}
      </header>
      {flush ? children : <div className="panel-body">{children}</div>}
      {footer && <div className="panel-footer">{footer}</div>}
    </section>
  );
}

export function Empty({ icon, children }: { icon?: ReactNode; children: ReactNode }) {
  return (
    <div className="empty">
      {icon}
      <p>{children}</p>
    </div>
  );
}

export function Badge({ tone = "neutral", dot = true, pulse, children }: { tone?: Tone; dot?: boolean; pulse?: boolean; children: ReactNode }) {
  return (
    <span className={`badge tone-${tone}${pulse ? " pulse" : ""}`}>
      {dot && <span className="dot" aria-hidden />}
      {children}
    </span>
  );
}

/** Labeled form control. Labels are always visible (never placeholder-only). */
export function Field({ label, hint, grow, children }: { label: string; hint?: string; grow?: boolean; children: ReactNode }) {
  return (
    <label className={`field${grow ? " grow" : ""}`}>
      <span>{label}</span>
      {children}
      {hint && <small className="hint">{hint}</small>}
    </label>
  );
}

/** Monospace value that copies itself to the clipboard on click. */
export function CopyText({ value, display, label, maxWidth }: { value: string; display?: string; label: string; maxWidth?: number }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const onClick = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setCopied(false), 1400);
    } catch {
      /* clipboard unavailable (non-secure context) */
    }
  };

  return (
    <button
      type="button"
      className={`copy${copied ? " copied" : ""}`}
      onClick={onClick}
      title={`${value}\n(click to copy)`}
      aria-label={copied ? `${label} copied` : `Copy ${label}: ${value}`}
      style={maxWidth ? { maxWidth } : undefined}
    >
      <span className="copy-text">{display ?? value}</span>
      {copied ? <Check size={12} aria-hidden /> : <Copy size={12} aria-hidden />}
    </button>
  );
}

export function Kbd({ keys }: { keys: string[] }) {
  return (
    <span className="kbd-hint" aria-hidden>
      {keys.map((k) => (
        <kbd key={k}>{k}</kbd>
      ))}
    </span>
  );
}

export function formatDate(value?: string | null): string {
  if (!value) return "—";
  return new Date(value).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

/** "3m ago", "2h ago", "5d ago" — falls back to a date for older values. */
export function relativeTime(value?: string | null): string {
  if (!value) return "—";
  const diff = (Date.now() - new Date(value).getTime()) / 1000;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)}d ago`;
  return new Date(value).toLocaleDateString();
}

export function shortSha(sha?: string | null): string {
  return sha ? sha.slice(0, 8) : "—";
}

export function label(value: string): string {
  return value.replace(/_/g, " ");
}
