import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { TaskReleasePanel } from "./TaskReleasePanel";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;
const sha = "ab".repeat(20);

describe("TaskReleasePanel", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows exactly what a push covers and approves it with the pinned hash", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(ok({
      request: { sha, branch: "wise/add-x", remote: "https://github.com/o/r.git", accountName: "gh", baseBranch: "main" },
      pushes: [], pullRequests: [],
      approvals: [{ id: "ap1", gate: "release", kind: "push", combinedHash: "h".repeat(64) }],
    }));
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok({}));
    render(<QueryClientProvider client={new QueryClient()}><TaskReleasePanel taskId="t1" /></QueryClientProvider>);
    expect(await screen.findByText("wise/add-x")).toBeTruthy();
    expect(screen.getByText("https://github.com/o/r.git")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Approve push/ }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    const [path, init] = post.mock.calls[0] as unknown as [string, { params: unknown; body: unknown }];
    expect(path).toBe("/approvals/{approvalId}/approve");
    expect(init.body).toEqual({ combinedHash: "h".repeat(64) });
  });

  it("shows the push and the pull request", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(ok({
      pushes: [{ id: "p1", status: "pushed", remote: "https://github.com/o/r.git", branch: "wise/add-x", sha, baseBranch: "main", attempts: 1, existed: false, createdAt: "2026-11-11T00:00:00Z" }],
      pullRequests: [{ id: "pr1", status: "opened", number: 7, url: "https://github.com/o/r/pull/7", state: "open", draft: true, title: "Add X", createdAt: "2026-11-11T00:00:00Z" }],
      approvals: [],
    }));
    render(<QueryClientProvider client={new QueryClient()}><TaskReleasePanel taskId="t1" /></QueryClientProvider>);
    expect(await screen.findByText("pushed")).toBeTruthy();
    expect(screen.getByText("draft")).toBeTruthy();
    expect(screen.getByRole("link", { name: /#7 Add X/ }).getAttribute("href")).toBe("https://github.com/o/r/pull/7");
  });
});
