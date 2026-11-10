import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { CodingRuntimesPanel } from "./CodingRuntimesPanel";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

describe("CodingRuntimesPanel", () => {
  afterEach(() => vi.restoreAllMocks());

  it("enables a runtime and warns before allowing subscription credentials", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(ok({ enabled: [], allowSubscription: false }));
    const put = vi.spyOn(api, "PUT").mockResolvedValue(ok({ enabled: ["claude-code"], allowSubscription: true }));
    render(<QueryClientProvider client={new QueryClient()}><CodingRuntimesPanel projectId="p1" readOnly={false} /></QueryClientProvider>);

    fireEvent.click(await screen.findByLabelText("Claude Code"));
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
    fireEvent.click(screen.getByLabelText(/Allow subscription credentials/));
    expect(screen.getByRole("note")).toHaveTextContent("any command it runs");

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const [path, init] = put.mock.calls[0] as unknown as [string, { body: unknown }];
    expect(path).toBe("/projects/{projectId}/coding-runtimes");
    expect(init.body).toEqual({ enabled: ["claude-code"], allowSubscription: true });
  });
});
