import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { InsightsPage } from "./InsightsPage";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

describe("InsightsPage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows agent metrics and rejection comments", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(ok({
      window: "30d", since: "2026-10-11T00:00:00Z",
      agents: [{ agent: "planner-agent", version: "1.2.0", definitionId: "d1", runs: 10, succeeded: 8, failed: 2, cancelled: 0, retries: 1,
        invalidOutputs: 1, medianSeconds: 30, p90Seconds: 120, costUsd: 0.5, tokens: 12000, reviewed: 4, rejected: 3, gateFixRounds: 0,
        thumbsUp: 1, thumbsDown: 2, successRate: 0.8, rejectionRate: 0.75, costPerRunUsd: 0.05, invalidRate: 0.1 }],
      providers: [], templates: [], stalls: [], failures: [],
      comments: [{ agent: "planner-agent", text: "Plan ignores migrations", count: 3 }],
    }));
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><InsightsPage /></MemoryRouter></QueryClientProvider>);
    expect(await screen.findByText("planner-agent", { selector: "a" })).toBeTruthy();
    expect(screen.getByText("3/4")).toBeTruthy();
    expect(screen.getByText("80%")).toBeTruthy();
    expect(screen.getByText("Plan ignores migrations")).toBeTruthy();
    expect(screen.getByText("No failed runs in this window.")).toBeTruthy();
  });
});
