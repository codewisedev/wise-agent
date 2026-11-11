import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ImprovementEditor } from "./SettingsPanels";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

describe("ImprovementEditor", () => {
  afterEach(() => vi.restoreAllMocks());

  it("renders a value saved before the eval section existed and saves it with defaults", () => {
    vi.spyOn(api, "GET").mockResolvedValue(ok({ items: [], builtinProviders: [] }));
    const set = vi.fn();
    const old = { schedule: { enabled: true, weekday: "monday", hour: 7 }, window_days: 30, min_runs: 3, max_proposals: 5,
      analysis: { account: "", model: "" } };
    render(<QueryClientProvider client={new QueryClient()}><ImprovementEditor value={old as never} set={set} /></QueryClientProvider>);
    const cases = screen.getByLabelText("Cases per evaluation (at most)") as HTMLInputElement;
    expect(cases.value).toBe("5");
    fireEvent.change(cases, { target: { value: "7" } });
    expect(set.mock.calls[0]?.[0]).toMatchObject({ eval: { max_cases: 7, cost_cap_usd: 2, judge: { account: "", model: "" } } });
  });
});
