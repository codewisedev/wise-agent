import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { RepoReleaseEditor } from "./RepoRelease";

const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) }) as never;

describe("RepoReleaseEditor", () => {
  afterEach(() => vi.restoreAllMocks());

  it("detects the remote and account, then saves", async () => {
    vi.spyOn(api, "GET").mockImplementation(((path: string) => Promise.resolve(path === "/accounts"
      ? ok({ items: [{ id: "a1", name: "gh", kind: "github" }, { id: "a2", name: "lm", kind: "openai_compatible" }], builtinProviders: [] })
      : ok({}))) as never);
    const post = vi.spyOn(api, "POST").mockResolvedValue(ok({ remote: "https://github.com/o/r.git", baseBranch: "main", accounts: ["a1"] }));
    const put = vi.spyOn(api, "PUT").mockResolvedValue(ok({}));
    render(<QueryClientProvider client={new QueryClient()}><RepoReleaseEditor repositoryId="r1" /></QueryClientProvider>);
    expect(await screen.findByRole("option", { name: "gh (github)" })).toBeTruthy();
    expect(screen.queryByRole("option", { name: /lm/ })).toBeNull(); // only git accounts
    fireEvent.click(screen.getByRole("button", { name: "Detect" }));
    await waitFor(() => expect((screen.getByLabelText("Remote (https)") as HTMLInputElement).value).toBe("https://github.com/o/r.git"));
    expect(post).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const [, init] = put.mock.calls[0] as unknown as [string, { body: unknown }];
    expect(init.body).toEqual({ remote: "https://github.com/o/r.git", accountId: "a1", baseBranch: "main" });
  });
});
