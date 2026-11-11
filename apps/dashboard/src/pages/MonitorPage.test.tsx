import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { MonitorPage } from "./MonitorPage";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;
const run = (over: object) => ({ id: "r1", agent: "backend-agent", status: "running", purpose: "work", runtime: "claude-code", attempt: 1, maxAttempts: 2,
  cancelling: false, createdAt: "2026-11-11T00:00:00Z", tokens: 1200, costUsd: 0.05, taskId: "t1", taskTitle: "Add X", ...over });

describe("MonitorPage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("flags stuck runs, lists waiting work and cancels", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(ok({
      at: "2026-11-11T00:10:00Z", stuck: 1, evals: [],
      running: [run({ stuck: "no activity for 12m0s", lastEvent: "tool.called", lastEventAt: "2026-11-11T00:00:00Z" })],
      queued: [run({ id: "r2", agent: "product-agent", status: "queued", runtime: "native" })],
      waiting: [{ id: "w1", taskId: "t2", taskTitle: "Fix Y", projectName: "Shop", status: "awaiting_retry", stage: "implement", error: "tests failed", since: "2026-11-11T00:00:00Z" }],
    }));
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok({}));
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><MonitorPage /></MemoryRouter></QueryClientProvider>);
    expect(await screen.findByText(/looks stuck: no activity for 12m0s/)).toBeTruthy();
    expect(screen.getByText("claude-code")).toBeTruthy();
    expect(screen.getByText("tests failed")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Cancel backend-agent run" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/agent-runs/{runId}/cancel", { params: { path: { runId: "r1" } } }));
  });
});
