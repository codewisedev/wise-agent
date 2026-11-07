import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LoginPage } from "./LoginPage";

describe("LoginPage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("renders the sign-in form when logged out", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ type: "about:blank", title: "Unauthorized", status: 401 }), {
        status: 401,
        headers: { "Content-Type": "application/problem+json" },
      }),
    );
    const router = createMemoryRouter([{ path: "/login", element: <LoginPage /> }], { initialEntries: ["/login"] });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
  });
});
