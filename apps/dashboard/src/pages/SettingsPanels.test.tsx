import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { SettingsPanels } from "./SettingsPanels";

const setting = {
  key: "workflow_defaults", title: "Workflow defaults", description: "Limits.", value: { max_revisions: 5 },
  default: { max_revisions: 5 }, version: 3, updatedAt: new Date().toISOString(),
};

describe("SettingsPanels", () => {
  afterEach(() => vi.restoreAllMocks());

  it("edits through the form or JSON and sends the loaded version", async () => {
    vi.spyOn(api, "GET").mockResolvedValue({ data: { items: [setting] }, response: new Response(null, { status: 200 }) } as never);
    const put = vi.spyOn(api, "PUT").mockResolvedValue({ data: { ...setting, version: 4 }, response: new Response(null, { status: 200 }) } as never);
    render(<QueryClientProvider client={new QueryClient()}><SettingsPanels only={["workflow_defaults"]} /></QueryClientProvider>);

    const input = await screen.findByLabelText(/Rejections allowed/);
    fireEvent.change(input, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: /JSON/ }));
    const json = screen.getByLabelText("Workflow defaults JSON") as HTMLTextAreaElement;
    expect(JSON.parse(json.value)).toEqual({ max_revisions: 2 });

    fireEvent.change(json, { target: { value: "{ nope" } });
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    expect(await screen.findByText(/Not valid JSON/)).toBeInTheDocument();
    expect(put).not.toHaveBeenCalled();

    fireEvent.change(json, { target: { value: '{"max_revisions": 1}' } });
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const [, init] = put.mock.calls[0] as unknown as [string, { body: unknown }];
    expect(init.body).toEqual({ value: { max_revisions: 1 }, version: 3 });
  });

  it("edits protected paths and Claude Code's tools in the panel", async () => {
    const execution = { key: "execution", title: "Execution", description: "", version: 1, updatedAt: new Date().toISOString(),
      value: { limits: { cpus: 2, memory_mb: 4096, pids: 1024 }, egress_allowlist: [], command_timeout_s: 600, setup_timeout_s: 900,
        max_output_kb: 64, retention_days: 7, max_fix_rounds: 3, branch_template: "wise/{slug}-{id}", commit_author: { name: "", email: "" },
        images: {} }, default: {} };
    const runtime = { enabled: true, model: "sonnet", tools: ["Bash", "Read"], credential: { mode: "subscription", account: "" },
      prices: { input_per_mtok: 3, output_per_mtok: 15 }, limits: { wall_clock_s: 1800, idle_s: 300, max_turns: 60, max_cost_usd: 3 } };
    const coding = { key: "coding_runtimes", title: "Coding runtimes", description: "", version: 1, updatedAt: new Date().toISOString(),
      value: { runtimes: { "claude-code": runtime } }, default: {} };
    vi.spyOn(api, "GET").mockResolvedValue({ data: { items: [execution, coding] }, response: new Response(null, { status: 200 }) } as never);
    render(<QueryClientProvider client={new QueryClient()}><SettingsPanels only={["execution", "coding_runtimes"]} /></QueryClientProvider>);

    const paths = await screen.findByLabelText(/^Protected paths/);
    fireEvent.change(paths, { target: { value: ".github/workflows/**\n**/.env" } });
    fireEvent.click(screen.getAllByRole("button", { name: /JSON/ })[0]!);
    expect(JSON.parse((screen.getByLabelText("Execution JSON") as HTMLTextAreaElement).value).protected_paths).toEqual([".github/workflows/**", "**/.env"]);

    const tools = screen.getByLabelText(/^Tools/) as HTMLInputElement;
    expect(tools.value).toBe("Bash, Read");
    expect(screen.getByRole("note")).toHaveTextContent("any command it runs");
  });
});
