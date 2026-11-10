import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ImprovementsPage } from "./ImprovementsPage";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

const proposal = {
  id: "p1", kind: "prompt", target: "planner-agent", title: "Always list migrations", rationale: "Three rejections mention migrations.",
  evidence: { run_ids: ["11111111-2222-3333-4444-555555555555"], approval_ids: ["aaaaaaaa-2222-3333-4444-555555555555"] },
  change: { prompt: "You plan work.\nAlways list migrations.\n" }, before: { prompt: "You plan work.\n" }, base: {},
  status: "proposed", createdAt: "2026-11-10T10:00:00Z", updatedAt: "2026-11-10T10:00:00Z",
};
const analysis = {
  id: "a1", trigger: "manual", windowDays: 30, status: "succeeded", proposals: 1, createdAt: "2026-11-10T09:59:00Z",
  refused: [{ kind: "prompt", target: "reviewer-agent", title: "Be lenient", reason: "reviewer-agent: the code reviewer is a quality gate" }],
};

describe("ImprovementsPage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows the diff and evidence, rejects with a note, lists refusals", async () => {
    vi.spyOn(api, "GET").mockImplementation(((path: string) =>
      Promise.resolve(path === "/improvements/analyses" ? ok({ items: [analysis] }) : ok({ items: [proposal], openCount: 1 }))) as never);
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok({ ...proposal, status: "rejected" }));
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><ImprovementsPage /></MemoryRouter></QueryClientProvider>);

    expect(await screen.findByText("Always list migrations")).toBeTruthy();
    const diff = screen.getByLabelText("Changes");
    expect(diff.querySelector(".add")?.textContent).toContain("Always list migrations.");
    expect(screen.getByText("run 11111111")).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Why not (optional)"), { target: { value: "we have no migrations" } });
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    const [path, init] = post.mock.calls[0] as unknown as [string, { params: unknown; body: unknown }];
    expect(path).toBe("/improvements/proposals/{proposalId}/reject");
    expect(init.body).toEqual({ note: "we have no migrations" });

    fireEvent.click(screen.getByRole("button", { name: "1 refused" }));
    expect(screen.getByText(/the code reviewer is a quality gate/)).toBeTruthy();
  });
});
