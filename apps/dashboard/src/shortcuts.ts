import { useEffect } from "react";

/**
 * Two-key "go to" shortcuts (e.g. "g p") plus single keys, vim/GitHub style.
 * Ignored while typing in form fields or when modifier keys are held.
 */
export function useShortcuts(bindings: Record<string, () => void>) {
  useEffect(() => {
    let prefix: string | null = null;
    let timer: number | undefined;

    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.isContentEditable || ["INPUT", "SELECT", "TEXTAREA"].includes(t.tagName))) return;

      const key = e.key.toLowerCase();
      const combo = prefix ? `${prefix} ${key}` : key;
      window.clearTimeout(timer);
      prefix = null;

      if (bindings[combo]) {
        e.preventDefault();
        bindings[combo]();
      } else if (Object.keys(bindings).some((b) => b.startsWith(`${key} `))) {
        prefix = key;
        timer = window.setTimeout(() => (prefix = null), 1000);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.clearTimeout(timer);
    };
  }, [bindings]);
}
