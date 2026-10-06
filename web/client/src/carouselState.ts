// Carousel state machine (07-web-client.md, Main.dc.html):
// 5 stages, 4 s per stage, one shared clock drives both the slide index and
// the progress bars — no second timer that could drift apart.

export interface CarouselState {
  index: number;
  /** 0..1 progress of the current stage */
  progress: number;
}

export const CAROUSEL_STAGES = 5;
export const STAGE_MS = 4000;

/** Advances the state by dt ms, wrapping around after the last stage. */
export function advance(
  state: CarouselState,
  dtMs: number,
  stages: number = CAROUSEL_STAGES,
  stageMs: number = STAGE_MS,
): CarouselState {
  if (dtMs <= 0) {
    return state;
  }
  let index = state.index;
  let progress = state.progress + dtMs / stageMs;
  while (progress >= 1) {
    progress -= 1;
    index = (index + 1) % stages;
  }
  return { index, progress };
}

/** Jumps to a stage (click on a progress bar). */
export function jumpTo(index: number, stages: number = CAROUSEL_STAGES): CarouselState {
  const safe = Math.round(index);
  return { index: ((safe % stages) + stages) % stages, progress: 0 };
}

/**
 * rAF-driven clock: the callback receives real elapsed time between frames,
 * so pauses (hover/focus) do not cause a jump on resume.
 */
export function createClock(
  onTick: (dtMs: number) => void,
  raf: (cb: (t: number) => void) => number = (cb) => requestAnimationFrame(cb),
): { start: () => void; stop: () => void } {
  let handle = 0;
  let last = 0;
  let running = false;
  const frame = (t: number) => {
    if (!running) {
      return;
    }
    if (last !== 0) {
      onTick(t - last);
    }
    last = t;
    handle = raf(frame);
  };
  return {
    start: () => {
      if (running) {
        return;
      }
      running = true;
      last = 0;
      handle = raf(frame);
    },
    stop: () => {
      running = false;
      last = 0;
      cancelAnimationFrame(handle);
    },
  };
}

/** true when the user asked the system to minimize motion. */
export function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}
