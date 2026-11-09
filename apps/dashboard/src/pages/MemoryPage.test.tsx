import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { MemoryPage } from "./MemoryPage";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;
const base = { scope: "project", projectId: "p1", projectName: "Shop", category: "api", source: "review_feedback", sourceRefs: { taskId: "t1" },
  version: 1, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() };
const existing = { ...base, id: "m0", kind: "decision", title: "Queue via outbox", body: "Use a Postgres outbox.", status: "active", rationale: "", source: "manual" };
const cand = { ...base, id: "m1", kind: "rule", title: "Cap retries at 5", body: "Retry at most 5 times.", status: "candidate", rationale: "Review: 'never retry forever'" };
const dup = { ...base, id: "m2", kind: "decision", title: "Outbox queue", body: "Postgres outbox.", status: "candidate", rationale: "", duplicateOf: "m0" };

describe("MemoryPage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("approves, edits-and-approves and flags duplicates in the inbox", async () => {
    vi.spyOn(api, "GET").mockImplementation(((path: string, init?: { params?: { query?: { status?: string } } }) => Promise.resolve(
      path === "/memories/inbox" ? ok({ candidates: 2 })
        : path === "/memories" ? ok({ items: init?.params?.query?.status === "candidate" ? [cand, dup] : [existing] })
          : ok({ items: [{ id: "p1", name: "Shop" }] }),
    )) as never);
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok({ ...cand, status: "active" }));
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={["/memory?tab=inbox"]}><MemoryPage /></MemoryRouter></QueryClientProvider>);

    const item = (await screen.findByText("Cap retries at 5")).closest("li")!;
    expect(within(item).getByText(/never retry forever/)).toBeInTheDocument();
    expect(await screen.findByText(/duplicate of “Queue via outbox”/)).toBeInTheDocument();

    fireEvent.click(within(item).getByRole("button", { name: /Edit & approve/ }));
    fireEvent.change(within(item).getByDisplayValue("Cap retries at 5"), { target: { value: "Cap webhook retries at 5" } });
    fireEvent.click(within(item).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    const [path, init] = post.mock.calls[0] as unknown as [string, { params: { path: { memoryId: string } }; body: { title: string; kind: string } }];
    expect(path).toBe("/memories/{memoryId}/approve");
    expect(init.params.path.memoryId).toBe("m1");
    expect(init.body).toMatchObject({ title: "Cap webhook retries at 5", kind: "rule", scope: "project", projectId: "p1" });
  });
});
