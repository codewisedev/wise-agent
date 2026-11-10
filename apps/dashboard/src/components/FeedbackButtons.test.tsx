import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import * as auth from "../auth";
import { FeedbackButtons } from "./FeedbackButtons";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

describe("FeedbackButtons", () => {
  afterEach(() => vi.restoreAllMocks());

  it("rates a run down and asks for a note", async () => {
    vi.spyOn(auth, "useSession").mockReturnValue({ data: { user: { id: "u1" } } } as never);
    vi.spyOn(api, "GET").mockResolvedValue(ok({ items: [] }));
    const put = vi.spyOn(api, "PUT").mockResolvedValue(ok({}));
    render(<QueryClientProvider client={new QueryClient()}><FeedbackButtons targetType="run" targetId="r1" /></QueryClientProvider>);

    fireEvent.click(await screen.findByRole("button", { name: "Bad result" }));
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const note = screen.getByLabelText("What was good or wrong");
    fireEvent.change(note, { target: { value: "ignored the plan" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    const [path, init] = put.mock.calls[1] as unknown as [string, { body: unknown }];
    expect(path).toBe("/feedback");
    expect(init.body).toEqual({ targetType: "run", targetId: "r1", rating: "down", note: "ignored the plan" });
  });
});
