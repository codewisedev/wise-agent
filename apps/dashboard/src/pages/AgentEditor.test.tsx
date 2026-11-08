import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Schemas } from "../api/client";
import { AgentEditor, lineDiff } from "./AgentEditor";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

const agent = {
  id: "00000000-0000-0000-0000-000000000001", name: "product-agent", version: "1.2.0", description: "d", enabled: true,
  modelAlias: "reasoning", tools: [], runCount: 0, definition: { name: "product-agent", enabled: true, model_config: { alias: "reasoning" } },
  prompt: "line one\nline two", shippedPrompt: "line one\nline two", contentHash: "abcdef0123456789", createdAt: new Date().toISOString(),
  cloudAllowed: true, source: "shipped",
} as unknown as Schemas["AgentDetail"];

describe("lineDiff", () => {
  it("marks added and removed lines", () => {
    expect(lineDiff("a\nb\nc", "a\nx\nc")).toEqual([
      { op: " ", line: "a" }, { op: "-", line: "b" }, { op: "+", line: "x" }, { op: " ", line: "c" },
    ]);
  });
});

describe("AgentEditor", () => {
  afterEach(() => vi.restoreAllMocks());

  it("saves the edited prompt with the definition and shows the diff", async () => {
    vi.spyOn(api, "GET").mockResolvedValue(ok({ items: [] }));
    const put = vi.spyOn(api, "PUT").mockResolvedValue(ok(agent));
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter><AgentEditor agent={agent} /></MemoryRouter>
      </QueryClientProvider>,
    );
    const save = screen.getByRole("button", { name: /Save/ });
    expect(save).toBeDisabled();
    fireEvent.change(screen.getByLabelText("System prompt"), { target: { value: "line one\nline 2" } });
    fireEvent.click(screen.getByRole("button", { name: /Show changes/ }));
    expect(screen.getByLabelText("Prompt diff").textContent).toContain("+ line 2");
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const [, init] = put.mock.calls[0] as unknown as [string, { body: { prompt: string; definition: unknown } }];
    expect(init.body).toEqual({ prompt: "line one\nline 2", definition: agent.definition });
  });
});
