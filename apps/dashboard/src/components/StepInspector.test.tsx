import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { StepInspector, type TemplateDef } from "./WorkflowFlow";

vi.spyOn(api, "GET").mockResolvedValue({ data: { items: [], secretsEnabled: true, builtinProviders: [] }, response: new Response(null, { status: 200 }) } as never);

const agents = [
  { id: "1", name: "product-agent", version: "1", description: "Analysis", enabled: true, modelAlias: "reasoning", tools: [], runCount: 0, produces: "product_analysis" },
  { id: "2", name: "requirements-agent", version: "1", description: "Requirements", enabled: true, modelAlias: "reasoning", tools: [], runCount: 0, produces: "requirements", consumes: { product_analysis: "product_analysis" } },
  { id: "3", name: "qa-agent", version: "1", description: "QA", enabled: false, modelAlias: "reasoning", tools: [], runCount: 0 },
];
const def: TemplateDef = {
  steps: [
    { id: "analysis", name: "Analysis", agent: "product-agent" },
    { id: "req", name: "Requirements", agent: "requirements-agent", review: true },
  ],
};
const wrap = (ui: ReactNode) => render(<QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>);

describe("StepInspector", () => {
  it("edits a template step: review pins earlier steps and agents show what they read", () => {
    const onChange = vi.fn();
    wrap(<StepInspector def={def} index={1} agents={agents} mode="template" onChange={onChange} />);
    expect(screen.getByText(/reads/)).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /qa-agent/ })).not.toBeInTheDocument(); // not executable yet
    fireEvent.click(screen.getByLabelText("Analysis"));
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ id: "req", reviewIncludes: ["analysis"] }));
    fireEvent.click(screen.getByLabelText(/Review the result/));
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ review: false }));
  });

  it("in task mode only changes this task's view of the step", () => {
    const onViewChange = vi.fn();
    wrap(<StepInspector def={def} index={0} agents={agents} mode="task" view={{ enabled: true, review: false }} onViewChange={onViewChange} />);
    expect(screen.queryByLabelText("Step id")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete step" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Run this step"));
    expect(onViewChange).toHaveBeenLastCalledWith({ enabled: false, review: false });
  });
});
