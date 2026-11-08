import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { Board, positionBetween, type BoardList } from "./Board";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;
const P = "11111111-1111-1111-1111-111111111111";
const task = (id: string, title: string, position: number) => ({
  task: { id, projectId: P, title, description: "", status: "open", createdAt: "", updatedAt: "", position, priority: "none", labels: [], source: "manual" },
  pendingApproval: false,
}) as never;
const lists: BoardList[] = [
  { id: "c1", name: "Backlog", position: 1, role: "backlog", wipLimit: 1, cards: [task("t1", "One", 1), task("t2", "Two", 2)] },
  { id: "c2", name: "Done", position: 2, role: "done", cards: [] },
];
const view = (ui: React.ReactNode) => render(<QueryClientProvider client={new QueryClient()}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>);

describe("Board", () => {
  afterEach(() => vi.restoreAllMocks());

  it("adds and renames lists and flags a WIP limit", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok({}));
    const put = vi.spyOn(api, "PUT").mockResolvedValue(ok({}));
    view(<Board lists={lists} projectId={P} queryKey={["board", P]} />);

    expect(screen.getByText("2 / 1")).toHaveClass("warn");

    fireEvent.click(screen.getByRole("button", { name: /Add list/ }));
    fireEvent.change(screen.getByLabelText("New list name"), { target: { value: "QA" } });
    fireEvent.click(screen.getByRole("button", { name: "Add list" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect((post.mock.calls[0] as unknown as [string, { body: unknown }])[1].body).toEqual({ name: "QA" });

    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    const input = screen.getByLabelText("List name");
    fireEvent.change(input, { target: { value: "Shipped" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const [, init] = put.mock.calls[0] as unknown as [string, { params: { path: { columnId: string } }; body: { name: string; role: string } }];
    expect(init.params.path.columnId).toBe("c2");
    expect(init.body).toMatchObject({ name: "Shipped", role: "done" });
  });

  it("is read-only without a project", () => {
    view(<Board lists={lists} queryKey={["all"]} />);
    expect(screen.queryByRole("button", { name: /Add list/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Add task/ })).toBeNull();
    expect(screen.getByRole("heading", { name: "Done" })).toBeInTheDocument();
  });

  it("places between neighbours", () => {
    expect(positionBetween(1, 2)).toBe(1.5);
    expect(positionBetween(undefined, 3)).toBe(2);
    expect(positionBetween(4)).toBe(5);
  });
});
