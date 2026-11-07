import { describe, expect, it } from "vitest";
import { cycleTheme, setTheme } from "./theme";

describe("theme", () => {
  it("persists and cycles dark → light → system → dark", () => {
    setTheme("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    cycleTheme();
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem("wise-theme")).toBe("light");
    cycleTheme();
    expect(document.documentElement.dataset.theme).toBe("system");
    cycleTheme();
    expect(document.documentElement.dataset.theme).toBe("dark");
  });
});
