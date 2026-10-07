import { describe, expect, it } from "vitest";
import { advance, jumpTo } from "./carouselState";
import { rectFromDrag, toFraction } from "./remarkMath";

describe("carousel", () => {
  it("advances by dt and wraps around", () => {
    expect(advance({ index: 0, progress: 0 }, 4000)).toEqual({ index: 1, progress: 0 });
    expect(advance({ index: 0, progress: 0 }, 2000).progress).toBe(0.5);
    expect(advance({ index: 4, progress: 0.5 }, 2000)).toEqual({ index: 0, progress: 0 });
    expect(advance({ index: 0, progress: 0 }, 25000)).toEqual({ index: 1, progress: 0.25 });
  });

  it("jumpTo clamps and wraps", () => {
    expect(jumpTo(3)).toEqual({ index: 3, progress: 0 });
    expect(jumpTo(7)).toEqual({ index: 2, progress: 0 });
    expect(jumpTo(-1)).toEqual({ index: 4, progress: 0 });
  });
});

describe("remarkMath", () => {
  it("normalizes drags in any direction with 4 decimals", () => {
    const r = rectFromDrag({ x0: 0.8, y0: 0.7, x1: 0.1, y1: 0.2 });
    expect(r).toEqual({ x: 0.1, y: 0.2, w: 0.7, h: 0.5 });
  });

  it("clamps to the page", () => {
    const r = rectFromDrag({ x0: -0.2, y0: -0.5, x1: 1.3, y1: 1.7 });
    expect(r).toEqual({ x: 0, y: 0, w: 1, h: 1 });
  });

  it("rounds to 4 digits", () => {
    const r = rectFromDrag({ x0: 0.123456, y0: 0.2, x1: 0.623461, y1: 0.9 });
    expect(r.x).toBe(0.1235);
    expect(r.w).toBe(0.5);
  });

  it("toFraction maps to element coordinates", () => {
    const rect = { left: 100, top: 50, width: 200, height: 400 } as DOMRect;
    expect(toFraction(150, 150, rect)).toEqual({ fx: 0.25, fy: 0.25 });
  });
});
