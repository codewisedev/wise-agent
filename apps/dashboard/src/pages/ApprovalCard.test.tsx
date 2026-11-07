import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ApprovalCard, type Approval } from "./workflowUi";

const hash = "a".repeat(64);
const approval: Approval = {
  id: "11111111-1111-4111-8111-111111111111",
  workflowRunId: "22222222-2222-4222-8222-222222222222",
  taskId: "33333333-3333-4333-8333-333333333333",
  gate: "architecture",
  round: 2,
  status: "pending",
  artifacts: [
    { artifactId: "44444444-4444-4444-8444-444444444444", kind: "plan", version: 3, hash: "b".repeat(64) },
  ],
  combinedHash: hash,
  notes: ["Architecture review flags a MATERIAL scope change"],
  createdAt: new Date().toISOString(),
};

function renderCard() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ApprovalCard approval={approval} />
    </QueryClientProvider>,
  );
}

describe("ApprovalCard", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows pinned versions and architect notes", () => {
    renderCard();
    expect(screen.getByText(/round 2/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Plan v3/ })).toBeInTheDocument();
    expect(screen.getByText(/MATERIAL scope change/)).toBeInTheDocument();
  });

  it("requires a comment before requesting changes", () => {
    renderCard();
    const reject = screen.getByRole("button", { name: /Request changes/ });
    expect(reject).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Comment/), { target: { value: "Split step S-2" } });
    expect(reject).toBeEnabled();
  });

  it("approves exactly the reviewed hash", async () => {
    const post = vi.spyOn(api, "POST").mockResolvedValue({
      data: { ...approval, status: "approved" },
      response: new Response(null, { status: 200 }),
    } as never);
    renderCard();
    fireEvent.click(screen.getByRole("button", { name: /Pass to next step/ }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, init] = post.mock.calls[0] as unknown as [string, unknown];
    expect(path).toBe("/approvals/{approvalId}/approve");
    expect(init).toMatchObject({ params: { path: { approvalId: approval.id } }, body: { combinedHash: hash } });
  });
});
