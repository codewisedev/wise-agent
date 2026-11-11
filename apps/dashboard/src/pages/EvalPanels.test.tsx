import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ProposalEvaluation } from "./EvalPanels";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

describe("ProposalEvaluation", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows per-case scores and counts without declaring a winner", async () => {
    const run = {
      id: "e1", proposalId: "p1", agentName: "planner-agent", status: "succeeded", costCapUsd: 2, createdAt: "2026-11-10T10:00:00Z",
      summary: { cases: 2, judged: 2, failed: 0, baselineMean: 2.5, candidateMean: 4, candidateBetter: 2, same: 0, candidateWorse: 0,
        baselineInvalid: 0, candidateInvalid: 0, candidateSecrets: 0, costUsd: 0.12 },
      results: [
        { id: "r1", caseKind: "must_fix", expectation: "List migrations", status: "done", baselineScore: 2, candidateScore: 4, reasoning: "B lists them",
          checks: { baseline: { valid: true }, candidate: { valid: true, emptyRequired: ["risks"] } } },
      ],
    };
    vi.spyOn(api, "GET").mockImplementation(((path: string) => Promise.resolve(path.endsWith("/evals") ? ok({ items: [run] }) : path === "/accounts" ? ok({ items: [], builtinProviders: [] })
      : ok({ evaluable: true, agent: "planner-agent", cases: 2, estimatedCostUsd: 0.2, choosesModel: true, defaults: { max_cases: 5, cost_cap_usd: 2, judge: {} } }))) as never);
    const p = { id: "p1", kind: "prompt", target: "planner-agent", title: "t", rationale: "r", evidence: {}, change: {}, before: {}, base: {},
      status: "evaluated", createdAt: "", updatedAt: "" } as never;
    render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><ProposalEvaluation p={p} /></MemoryRouter></QueryClientProvider>);
    expect(await screen.findByText(/proposal better in 2, same in 0, worse in 0/)).toBeTruthy();
    expect(screen.getByText(/tendencies, not proof/)).toBeTruthy();
    expect(screen.getByText("empty: risks")).toBeTruthy();
    expect(screen.getByText("B lists them")).toBeTruthy();
    expect(screen.getByText(/Evaluate on 2 cases of planner-agent/)).toBeTruthy();
  });
});
