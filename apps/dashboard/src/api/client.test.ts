import { describe, expect, it } from "vitest";
import { ApiError, formatProblem } from "./client";

describe("formatProblem", () => {
  it("falls back to the status when there is no problem body", () => {
    expect(formatProblem(502)).toBe("Request failed (502)");
  });

  it("includes detail and field errors", () => {
    const msg = formatProblem(400, {
      title: "Bad Request",
      status: 400,
      type: "about:blank",
      detail: "request validation failed",
      errors: [{ field: "name", message: "must not be blank" }],
    });
    expect(msg).toBe("request validation failed — name: must not be blank");
  });

  it("ApiError carries status and message", () => {
    const err = new ApiError(404, { title: "Not Found", status: 404, type: "about:blank", detail: "project not found" });
    expect(err.status).toBe(404);
    expect(err.message).toBe("project not found");
  });
});
