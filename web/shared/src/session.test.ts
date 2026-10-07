import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import {
  expireSession,
  isCurrentSession,
  isStaleSession,
  runInSession,
  sessionGeneration,
  tagSessionError,
} from "./auth";
import { type JobDetail, setJobCache } from "./jobs";

// Session instance generation (07-web-client.md): a request captures the
// generation at start; callbacks of an old instance are ignored.

const detail = {
  id: "j1",
  title: "t",
  client_status: "done",
  status_text: "Готово",
  current_version: 1,
  created_at: "2026-10-07T00:00:00Z",
  updated_at: "2026-10-07T00:00:00Z",
} as unknown as JobDetail;

describe("session generation", () => {
  it("expireSession bumps the generation and fences the old one", () => {
    const client = new QueryClient();
    const old = sessionGeneration();
    expireSession(client);
    expect(isCurrentSession(old)).toBe(false);
    expect(isCurrentSession(sessionGeneration())).toBe(true);
  });

  it("tags and recognizes stale errors, fresh ones are not stale", () => {
    const client = new QueryClient();
    const old = sessionGeneration();
    expireSession(client);
    const stale = tagSessionError(new Error("late"), old);
    expect(isStaleSession(stale)).toBe(true);
    const fresh = tagSessionError(new Error("now"), sessionGeneration());
    expect(isStaleSession(fresh)).toBe(false);
    expect(isStaleSession(new Error("untagged"))).toBe(false);
  });

  it("runInSession tags thrown errors with the generation at start", async () => {
    const client = new QueryClient();
    const captured = sessionGeneration();
    expireSession(client);
    // A request started before the expiry delivers its error after it.
    await expect(
      runInSession(() => Promise.reject(new Error("late failure")), captured),
    ).rejects.toThrow("late failure");
    try {
      await runInSession(() => Promise.reject(new Error("late failure")), captured);
    } catch (error) {
      expect(isStaleSession(error)).toBe(true);
    }
    // Same-session failures stay current.
    try {
      await runInSession(() => Promise.reject(new Error("now failure")));
    } catch (error) {
      expect(isStaleSession(error)).toBe(false);
    }
  });

  it("setJobCache ignores writes of a stale session generation", () => {
    const client = new QueryClient();
    const old = sessionGeneration();
    expireSession(client);
    setJobCache(client, "u1", "j1", detail, old);
    expect(client.getQueryData(["job", "u1", "j1"])).toBeUndefined();
    setJobCache(client, "u1", "j1", detail);
    expect(client.getQueryData(["job", "u1", "j1"])).toEqual(detail);
  });

  it("setJobCache writes only for the matching job id", () => {
    const client = new QueryClient();
    setJobCache(client, "u1", "j1", { ...detail, id: "other" });
    expect(client.getQueryData(["job", "u1", "j1"])).toBeUndefined();
  });
});
