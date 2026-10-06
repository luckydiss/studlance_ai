import { describe, expect, it, vi } from "vitest";
import { type FetchLike, type UploadEntry, progressPercent, runUploads } from "./uploadQueue";

function entry(path: string, size: number): UploadEntry {
  const file = new File(["x".repeat(size)], path.split("/").pop() ?? path);
  Object.defineProperty(file, "size", { value: size });
  return { path, file };
}

describe("runUploads", () => {
  it("uploads all files and reports progress by bytes", async () => {
    const calls: string[] = [];
    const fetchImpl: FetchLike = async (input) => {
      calls.push(input);
      return new Response("{}", { status: 200 });
    };
    const progress: number[] = [];
    const out = await runUploads("j1", [entry("a.txt", 100), entry("b.txt", 300)], {
      fetchImpl,
      onProgress: (p) => progress.push(progressPercent(p)),
      wait: async () => {},
    });
    expect(out.failed).toHaveLength(0);
    expect(calls).toHaveLength(2);
    expect(calls[0]).toContain("/api/client/jobs/j1/input?path=a.txt");
    expect(progress.at(-1)).toBe(100);
  });

  it("retries up to 3 times on 500, then succeeds", async () => {
    let attempt = 0;
    const fetchImpl: FetchLike = async () => {
      attempt += 1;
      if (attempt < 3) {
        return new Response("err", { status: 500 });
      }
      return new Response("{}", { status: 200 });
    };
    const out = await runUploads("j1", [entry("a.txt", 1)], { fetchImpl, wait: async () => {} });
    expect(out.succeeded).toHaveLength(1);
    expect(attempt).toBe(3);
  });

  it("gives up after 3 retries with a failure entry", async () => {
    const fetchImpl: FetchLike = vi.fn(async () => {
      throw new TypeError("network down");
    });
    const out = await runUploads("j1", [entry("a.txt", 1)], { fetchImpl, wait: async () => {} });
    expect(out.failed).toHaveLength(1);
    expect(out.failures[0]?.name).toBe("a.txt");
    // 1 initial attempt + 3 retries
    expect(fetchImpl).toHaveBeenCalledTimes(4);
  });

  it("does not retry a permanent 413", async () => {
    const fetchImpl: FetchLike = vi.fn(
      async () =>
        new Response('{"error":{"code":"too_large","message":"Превышен лимит"}}', {
          status: 413,
          headers: { "Content-Type": "application/json" },
        }),
    );
    const out = await runUploads("j1", [entry("a.txt", 1)], { fetchImpl, wait: async () => {} });
    expect(out.failed).toHaveLength(1);
    expect(out.failures[0]?.message).toBe("Превышен лимит");
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it("runs at most 3 files in parallel", async () => {
    let inFlight = 0;
    let peak = 0;
    const fetchImpl: FetchLike = async () => {
      inFlight += 1;
      peak = Math.max(peak, inFlight);
      await new Promise((r) => setTimeout(r, 5));
      inFlight -= 1;
      return new Response("{}", { status: 200 });
    };
    const entries = [1, 2, 3, 4, 5].map((i) => entry(`f${i}.txt`, 1));
    const out = await runUploads("j1", entries, { fetchImpl });
    expect(out.succeeded).toHaveLength(5);
    expect(peak).toBeLessThanOrEqual(3);
  });
});

describe("progressPercent", () => {
  it("computes percent of done bytes", () => {
    expect(progressPercent({ statuses: {}, doneBytes: 90, totalBytes: 200, failures: [] })).toBe(
      45,
    );
    expect(progressPercent({ statuses: {}, doneBytes: 0, totalBytes: 0, failures: [] })).toBe(100);
  });
});
