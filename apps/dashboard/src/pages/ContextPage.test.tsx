import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ContextPage } from "./ContextPage";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;
const hit = {
  kind: "symbol", repository: "api", repositoryId: "r1", path: "gateway.go", startLine: 2, endLine: 3,
  title: "method Gateway.Reconfigure", snippet: "func (g *Gateway) Reconfigure()", score: 1, why: "exact name", indexedSha: "abc", stale: true,
};

describe("ContextPage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("searches the index and opens the real file at the hit", async () => {
    const get = vi.spyOn(api, "GET").mockImplementation(((path: string) => Promise.resolve(
      path === "/projects/{projectId}" ? ok({ id: "p1", name: "Wise" })
        : path === "/projects/{projectId}/repositories" ? ok({ items: [{ id: "r1", name: "api", indexedSha: "abc" }] })
          : path === "/projects/{projectId}/context/search" ? ok({ items: [hit], note: "Some results come from an older commit" })
            : path === "/repositories/{repositoryId}/file" ? ok({ path: "gateway.go", content: "package api\nfunc (g *Gateway) Reconfigure() {}\n", lines: 3, truncated: false, headSha: "def" })
              : ok({ items: [] }),
    )) as never);
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={["/projects/p1/context"]}>
          <Routes><Route path="/projects/:projectId/context" element={<ContextPage />} /></Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    fireEvent.change(await screen.findByLabelText("Search the code index"), { target: { value: "Reconfigure" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText("method Gateway.Reconfigure")).toBeInTheDocument();
    expect(screen.getByText(/older commit/)).toBeInTheDocument();
    const call = get.mock.calls.find((c) => c[0] === "/projects/{projectId}/context/search") as unknown as [string, { params: { query: { q: string } } }];
    expect(call[1].params.query.q).toBe("Reconfigure");

    fireEvent.click(screen.getByRole("button", { name: /api\/gateway\.go/ }));
    const view = await screen.findByLabelText("File content");
    await waitFor(() => expect(view.querySelector(".hl")?.textContent).toContain("Reconfigure"));
    expect(screen.getByText("index older than HEAD")).toBeInTheDocument();
  });
});
