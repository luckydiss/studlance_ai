// Remark rectangle math (07-web-client.md):
// coordinates are fractions of the displayed image, x/y/w/h in 0..1 with
// 4 decimal digits; the rectangle never crosses the page border.

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface RectInput {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
}

function round4(v: number): number {
  return Math.round(v * 10000) / 10000;
}

function clamp(v: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, v));
}

/**
 * Builds a normalized rect from two drag corners (any direction), clamped to
 * the 0..1 page. Points are already fractions of the image.
 */
export function rectFromDrag(input: RectInput): Rect {
  const x = clamp(Math.min(input.x0, input.x1), 0, 1);
  const y = clamp(Math.min(input.y0, input.y1), 0, 1);
  const x2 = clamp(Math.max(input.x0, input.x1), 0, 1);
  const y2 = clamp(Math.max(input.y0, input.y1), 0, 1);
  return {
    x: round4(x),
    y: round4(y),
    w: round4(x2 - x),
    h: round4(y2 - y),
  };
}

/** Converts pointer coordinates to fractions of an element's box. */
export function toFraction(
  clientX: number,
  clientY: number,
  rect: DOMRect,
): { fx: number; fy: number } {
  return {
    fx: (clientX - rect.left) / rect.width,
    fy: (clientY - rect.top) / rect.height,
  };
}

/** Minimal drag size so a stray click does not create a remark. */
export function isMeaningfulDrag(input: RectInput, minPx: number, rect: DOMRect): boolean {
  const dx = Math.abs(input.x1 - input.x0) * rect.width;
  const dy = Math.abs(input.y1 - input.y0) * rect.height;
  return dx >= minPx && dy >= minPx;
}
