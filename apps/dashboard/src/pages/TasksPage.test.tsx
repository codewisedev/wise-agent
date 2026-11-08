import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import type { BoardList } from "../components/Board";
import { TaskDrawer } from "./TaskDrawer";
import { mergeBoards } from "./TasksPage";

const card = (id: string) => ({ task: { id, title: id }, pendingApproval: false }) as never;
const col = (id: string, name: string, cards: string[]): BoardList => ({ id, name, position: 1, cards: cards.map(card) });

describe("mergeBoards", () => {
  it("combines lists by name across projects, keeping board order", () => {
    const merged = mergeBoards([
      { columns: [col("a1", "Backlog", ["x"]), col("a2", "Done", ["y"])] },
      { columns: [col("b1", "backlog ", ["z"]), col("b2", "QA", []), col("b3", "Done", [])] },
    ]);
    expect(merged.map((c) => c.name)).toEqual(["Backlog", "QA", "Done"]);
    expect(merged[0]!.cards.map((c) => c.task.id)).toEqual(["x", "z"]);
    expect(merged[0]!.id).toBe("all:backlog");
  });
});

describe("TaskDrawer", () => {
  afterEach(() => vi.restoreAllMocks());

  it("edits a card and starts its workflow", async () => {
    const task = { id: "t1", projectId: "p", title: "Ship it", description: "", status: "open", priority: "none", labels: ["ui"], createdAt: "", updatedAt: "" };
    const res = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;
    vi.spyOn(api, "GET").mockImplementation(((path: string) => Promise.resolve(
      path === "/projects/{projectId}/columns" ? res({ items: [{ id: "c1", projectId: "p", name: "Backlog", position: 1 }] })
        : path === "/projects/{projectId}/board" ? res({ columns: [] })
          : path === "/projects/{projectId}/sprints" ? res({ items: [] })
          : res({ task: { ...task, column: "c1" }, plan: { steps: [] }, issues: [], steps: [], artifacts: [], approvals: [] }),
    )) as never);
    const patch = vi.spyOn(api, "PATCH").mockResolvedValue({ data: task, response: new Response(null, { status: 200 }) } as never);
    const post = vi.spyOn(api, "POST").mockResolvedValue({ data: {}, response: new Response(null, { status: 202 }) } as never);
    const onClose = vi.fn();
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><TaskDrawer taskId="t1" onClose={onClose} invalidate={[]} /></MemoryRouter></QueryClientProvider>);

    const title = await screen.findByDisplayValue("Ship it");
    fireEvent.change(title, { target: { value: "Ship it now" } });
    fireEvent.change(screen.getByDisplayValue("none"), { target: { value: "high" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove label ui" }));
    const add = screen.getByLabelText("Add label");
    fireEvent.change(add, { target: { value: "backend" } });
    fireEvent.keyDown(add, { key: "Enter" });
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
    expect((patch.mock.calls[0] as unknown as [string, { body: unknown }])[1].body).toEqual({ title: "Ship it now", description: "", priority: "high", labels: ["backend"] });

    fireEvent.click(screen.getByRole("button", { name: /Step by step/ }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect((post.mock.calls[0] as unknown as [string, { body: unknown }])[1].body).toEqual({ stepByStep: true });

    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });
});
