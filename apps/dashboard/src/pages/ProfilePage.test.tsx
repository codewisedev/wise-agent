import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ProfilePage } from "./ProfilePage";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;
const now = new Date().toISOString();
const pref = { id: "m1", scope: "developer", kind: "preference", category: "testing", title: "Vitest for unit tests", body: "Use Vitest.",
  status: "active", source: "profile_inference", sourceRefs: {}, rationale: "", version: 2, createdAt: now, updatedAt: now };
const rule = { ...pref, id: "m2", kind: "rule", category: "coding", title: "No default exports", body: "Named exports only.", source: "manual" };

describe("ProfilePage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("groups the profile by area and promotes a preference to a rule explicitly", async () => {
    vi.spyOn(api, "GET").mockImplementation(((path: string, init?: { params?: { query?: { kind?: string } } }) => Promise.resolve(
      path === "/memories" ? ok({ items: init?.params?.query?.kind === "example" ? [] : [pref, rule] }) : ok({ items: [{ id: "p1", name: "Shop" }] }),
    )) as never);
    const put = vi.spyOn(api, "PUT").mockResolvedValue(ok({ ...pref, kind: "rule" }));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><ProfilePage /></MemoryRouter></QueryClientProvider>);

    const testing = await screen.findByRole("region", { name: "Testing Preferences" });
    expect(within(testing).getByText("inferred from your repositories")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Coding standards Rules" })).toHaveTextContent("No default exports");

    fireEvent.click(within(testing).getByRole("button", { name: /Make it a rule/ }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const [, init] = put.mock.calls[0] as unknown as [string, { params: { path: { memoryId: string } }; body: { kind: string; title: string } }];
    expect(init.params.path.memoryId).toBe("m1");
    expect(init.body).toMatchObject({ kind: "rule", title: "Vitest for unit tests", scope: "developer" });
  });
});
