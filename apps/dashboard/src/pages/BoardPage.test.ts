import { describe, expect, it } from "vitest";
import { positionBetween } from "./BoardPage";

describe("positionBetween", () => {
  it("places cards between, before and after neighbours", () => {
    expect(positionBetween(1, 2)).toBe(1.5);
    expect(positionBetween(undefined, 3)).toBe(2);
    expect(positionBetween(5, undefined)).toBe(6);
    expect(positionBetween()).toBe(1);
  });
  it("keeps order when inserting repeatedly at the same spot", () => {
    let hi = 2;
    for (let i = 0; i < 30; i++) {
      const p = positionBetween(1, hi);
      expect(p).toBeGreaterThan(1);
      expect(p).toBeLessThan(hi);
      hi = p;
    }
  });
});
