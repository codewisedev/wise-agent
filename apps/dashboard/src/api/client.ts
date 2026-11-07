import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema.gen";

export type Schemas = components["schemas"];
export type Project = Schemas["Project"];
export type Repository = Schemas["Repository"];
export type Problem = Schemas["Problem"];

let csrfToken: string | undefined;

/** Stores the session's CSRF token; it is sent on every state-changing request. */
export function setCsrfToken(token: string | undefined) {
  csrfToken = token;
}

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

const csrfMiddleware: Middleware = {
  onRequest({ request }) {
    if (csrfToken && !SAFE_METHODS.has(request.method)) {
      request.headers.set("X-Wise-CSRF", csrfToken);
    }
    return request;
  },
  onResponse({ response }) {
    if (response.status === 401) {
      window.dispatchEvent(new Event("wise:unauthorized"));
    }
    return response;
  },
};

export const api = createClient<paths>({ baseUrl: "/api/v1", credentials: "same-origin" });
api.use(csrfMiddleware);

export class ApiError extends Error {
  readonly status: number;
  readonly problem?: Problem;

  constructor(status: number, problem?: Problem) {
    super(formatProblem(status, problem));
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
  }
}

export function formatProblem(status: number, problem?: Partial<Problem>): string {
  if (!problem) return `Request failed (${status})`;
  const fields = problem.errors?.map((e) => `${e.field}: ${e.message}`).join("; ");
  return [problem.detail ?? problem.title ?? `Request failed (${status})`, fields].filter(Boolean).join(" — ");
}

/** Resolves an openapi-fetch result to its data, throwing ApiError on failure. */
export async function unwrap<T>(
  promise: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const { data, error, response } = await promise;
  if (!response.ok) {
    throw new ApiError(response.status, error as Problem | undefined);
  }
  return data as T;
}
