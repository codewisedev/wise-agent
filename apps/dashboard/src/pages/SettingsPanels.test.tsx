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
});
