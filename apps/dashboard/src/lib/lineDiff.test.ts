import { describe, expect, it } from "vitest";
import { lineDiff, withContext } from "./lineDiff";

describe("lineDiff", () => {
  it("finds added and removed lines", () => {
    const d = lineDiff("a\nb\nc", "a\nx\nc\nd");
    expect(d).toEqual([
      { op: "same", text: "a" }, { op: "del", text: "b" }, { op: "add", text: "x" }, { op: "same", text: "c" }, { op: "add", text: "d" },
    ]);
  });
  it("collapses long unchanged runs", () => {
    const before = Array.from({ length: 20 }, (_, i) => `l${i}`).join("\n");
    const out = withContext(lineDiff(before, before + "\nnew"), 2);
    expect(out[0]).toEqual({ op: "gap", count: 18 });
    expect(out.at(-1)).toEqual({ op: "add", text: "new" });
  });
});
