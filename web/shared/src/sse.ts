import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { type CurrentUser, useCurrentUser } from "./auth";
import { type JobDetail, setJobCache } from "./jobs";
import { jobStreamUrl } from "./paths";

// SSE subscription for a job page (07-web-client.md):
// - the stream sends a full JobDetail: first `snapshot`, then `job` on every
//   change; `: ping` comments are handled by EventSource itself;
// - both events replace the cached detail, so after a reconnect the fresh
//   snapshot restores the actual state;
// - on drop the hook reconnects with a 1→2→5→10 s pause;
// - unmount closes the EventSource and clears pending timers;
// - the subscription belongs to the current user: when the session ends or
//   the account changes the subscription closes, and events of the old
//   session are ignored (they can only write into the old user's key while
//   no session is active).

const RECONNECT_STEPS_MS = [1000, 2000, 5000, 10000];

export function useJobStream(jobId: string, enabled: boolean) {
  const queryClient = useQueryClient();
  const { data: user } = useCurrentUser();
  const attemptRef = useRef(0);
  const timerRef = useRef<number | undefined>(undefined);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const userId = user?.id ?? null;
    let disposed = false;
    let source: EventSource | null = null;

    const clearTimer = () => {
      if (timerRef.current !== undefined) {
        window.clearTimeout(timerRef.current);
        timerRef.current = undefined;
      }
    };

    const sessionStillCurrent = () => {
      const current = queryClient.getQueryData<CurrentUser | null>(["me"]);
      return current?.id === userId;
    };

    const applyDetail = (raw: string) => {
      try {
        const detail = JSON.parse(raw) as JobDetail;
        if (detail && detail.id === jobId && sessionStillCurrent()) {
          attemptRef.current = 0;
          setJobCache(queryClient, userId, jobId, detail);
        }
      } catch {
        // A partial write between events: the next event replaces the cache.
      }
    };

    const connect = () => {
      if (disposed || !sessionStillCurrent()) {
        return;
      }
      source = new EventSource(jobStreamUrl(jobId));
      source.addEventListener("snapshot", (ev) => applyDetail((ev as MessageEvent).data));
      source.addEventListener("job", (ev) => applyDetail((ev as MessageEvent).data));
      source.onerror = () => {
        // EventSource retries on its own; we take over with backoff instead.
        source?.close();
        source = null;
        if (disposed) {
          return;
        }
        const delay =
          RECONNECT_STEPS_MS[Math.min(attemptRef.current, RECONNECT_STEPS_MS.length - 1)];
        attemptRef.current += 1;
        clearTimer();
        timerRef.current = window.setTimeout(connect, delay);
      };
    };

    connect();
    return () => {
      disposed = true;
      clearTimer();
      source?.close();
    };
  }, [jobId, enabled, user?.id, queryClient]);
}
