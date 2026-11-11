import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ProjectActivity } from "./ProjectActivity";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

describe("ProjectActivity", () => {
  afterEach(() => vi.restoreAllMocks());

  it("lists events with links and filters by kind", async () => {
    const get = vi.spyOn(api, "GET").mockResolvedValue(ok({ items: [
      { at: "2026-11-11T10:00:00Z", kind: "branch.pushed", title: "Add X", detail: "wise/add-x → https://github.com/o/r.git", taskId: "t1", refId: "p1", url: "https://github.com/o/r/tree/wise/add-x" },
      { at: "2026-11-11T09:00:00Z", kind: "approval.rejected", title: "Add X", detail: "requirements: needs audit logging", taskId: "t1", refId: "a1", url: "" },
    ], next: null }));
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><ProjectActivity projectId="p" /></MemoryRouter></QueryClientProvider>);
    expect(await screen.findByText("branch pushed")).toBeTruthy();
    expect(screen.getByText("changes requested")).toBeTruthy();
    expect(screen.getByRole("link", { name: /open/ }).getAttribute("href")).toBe("https://github.com/o/r/tree/wise/add-x");
    fireEvent.click(screen.getByRole("button", { name: "approvals" }));
    await waitFor(() => {
      const last = get.mock.calls.at(-1) as unknown as [string, { params: { query: { kinds?: string } } }];
      expect(last[1].params.query.kinds).toBe("approval");
    });
  });
});
