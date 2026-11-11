import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { CostsPage } from "./CostsPage";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

describe("CostsPage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("stacks spend per day, shows the day on focus, and has a table view", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(ok({
      window: "7d", groupBy: "runtime", keys: ["native", "claude-code"], total: 1.5, evalUsd: 0.2, tokens: 3000, calls: 4,
      billing: { subscription: 1.0, api: 0.5, local: 0 },
      days: [{ day: "2026-11-10", total: 0, by: {} }, { day: "2026-11-11", total: 1.5, by: { native: 0.5, "claude-code": 1.0 } }],
      budgets: [{ agent: "product-agent", spent: 4.5, budget: 5 }],
      topTasks: [{ id: "t1", title: "Add X", costUsd: 1.2, runs: 6 }],
    }));
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><CostsPage /></MemoryRouter></QueryClientProvider>);
    const bar = await screen.findByLabelText(/2026-11-11: /);
    expect(screen.getByRole("img").getAttribute("aria-label")).toContain("stacked by runtime");
    fireEvent.focus(bar);
    expect(screen.getByRole("status").textContent).toContain("claude-code");
    expect(screen.getByText("subscription (API-equivalent)")).toBeTruthy();
    expect(screen.getByRole("meter", { name: "product-agent spend today" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "table" }));
    expect(screen.getByRole("columnheader", { name: "claude-code" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Add X" })).toBeTruthy();
  });
});
