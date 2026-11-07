import { useSyncExternalStore } from "react";

export type ThemePreference = "dark" | "light" | "system";
const KEY = "wise-theme";
const ORDER: ThemePreference[] = ["dark", "light", "system"];

function read(): ThemePreference {
  const v = document.documentElement.dataset.theme;
  return v === "light" || v === "system" ? v : "dark";
}

const listeners = new Set<() => void>();

/** Sets the theme on <html>, persists it and notifies every subscriber. */
export function setTheme(pref: ThemePreference) {
  document.documentElement.dataset.theme = pref;
  try {
    localStorage.setItem(KEY, pref);
  } catch {
    /* storage unavailable */
  }
  listeners.forEach((l) => l());
}

export function cycleTheme() {
  setTheme(ORDER[(ORDER.indexOf(read()) + 1) % ORDER.length]!);
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/**
 * Shared theme preference. The initial value is applied before first paint by
 * public/theme-init.js (default "dark"); <html data-theme> is the single source of truth.
 */
export function useTheme() {
  const pref = useSyncExternalStore(subscribe, read, () => "dark" as ThemePreference);
  return { pref, setPref: setTheme, cycle: cycleTheme };
}
