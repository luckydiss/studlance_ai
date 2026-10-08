import {
  type CurrentUser,
  adminJobStreamUrl,
  api,
  isCurrentSession,
  isStaleSession,
  runInSession,
  sessionGeneration,
  useCurrentUser,
} from "@studlance/shared";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { requireData, useAdminError } from "./api";
import { StaleAdminViewError, adminJobKey } from "./session";

// A trace step in the admin view. Both sources are normalised into it:
//   - HTTP history: {seq, ts, type, summary, payload(object)}
//   - SSE steps:    {Seq, Ts, Type, Summary, Payload(string)} + run_id
export interface AdminStep {
  seq: number;
  ts: string;
  type: string;
  summary: string;
  payload: unknown;
}

export type StepMap = Record<string, AdminStep[]>;

interface RawSseStep {
  seq?: number;
  ts?: string;
  type?: string;
  summary?: string;
  payload?: unknown;
  // Some older worker events used Go field names; accept both wire shapes.
  Seq?: number;
  Ts?: string;
  Type?: string;
  Summary?: string;
  Payload?: unknown;
}

const PAGE = 500;

function parsePayload(value: unknown): unknown {
  if (value === null || value === undefined || value === "") {
    return null;
  }
  if (typeof value === "object") {
    return value;
  }
  if (typeof value === "string") {
    try {
      return JSON.parse(value);
    } catch {
      return value;
    }
  }
  return null;
}

/** Inserts a step keeping the list ordered by seq without duplicating. */
function insertStep(list: AdminStep[], step: AdminStep): AdminStep[] {
  let low = 0;
  let high = list.length;
  while (low < high) {
    const middle = (low + high) >>> 1;
    const current = list[middle];
    if (current && current.seq < step.seq) {
      low = middle + 1;
    } else {
      high = middle;
    }
  }
  if (list[low]?.seq === step.seq) return list;
  const next = list.slice();
  next.splice(low, 0, step);
  return next;
}

/**
 * Loads the whole trace of every run via HTTP pages and merges the live SSE
 * steps. Detail (snapshot/job) never replaces the step history.
 */
export function useAdminJob(
  jobId: string,
  userId: string | null | undefined,
  viewToken: symbol,
  isViewCurrent: (token: symbol) => boolean,
) {
  const queryClient = useQueryClient();
  const me = useCurrentUser();
  const generation = sessionGeneration();
  const enabled = me.data?.role === "admin";
  const onError = useAdminError();

  const query = useQuery({
    queryKey: adminJobKey(userId, jobId),
    enabled,
    refetchInterval: 15_000,
    queryFn: async () => {
      try {
        const detail = await runInSession(async () =>
          requireData(await api.GET("/api/admin/jobs/{id}", { params: { path: { id: jobId } } })),
        );
        if (!isViewCurrent(viewToken)) throw new StaleAdminViewError(null);
        return detail;
      } catch (error) {
        if (!isViewCurrent(viewToken)) throw new StaleAdminViewError(error);
        throw error;
      }
    },
  });

  const [steps, setSteps] = useState<StepMap>({});
  const [connected, setConnected] = useState(false);
  const [historyError, setHistoryError] = useState<unknown>(null);
  const [historyLoading, setHistoryLoading] = useState(false);
  // Highest seq already loaded from HTTP per run; live steps never advance it.
  const loadedThrough = useRef<Record<string, number>>({});
  const loadingRuns = useRef<Set<string>>(new Set());
  const refreshAfterLoad = useRef(false);
  const loadHistoryRef = useRef<(() => void) | null>(null);

  // Reset everything when the job or the session instance changes, so late
  // data of a previous card or session cannot leak in. `generation` is the
  // explicit trigger: a new session instance must clear the trace.
  // biome-ignore lint/correctness/useExhaustiveDependencies: generation is the intended reset trigger.
  useEffect(() => {
    setSteps({});
    setConnected(false);
    loadedThrough.current = {};
    loadingRuns.current = new Set();
    refreshAfterLoad.current = false;
    setHistoryError(null);
    setHistoryLoading(false);
  }, [jobId, generation]);

  const runIds = (query.data?.agent_runs ?? []).map((run) => run.id).join(",");

  // Load the missing history for every known run, page by page.
  useEffect(() => {
    if (!enabled || !runIds) {
      loadHistoryRef.current = null;
      return;
    }
    let cancelled = false;
    const ids = runIds.split(",").filter(Boolean);
    const stillCurrent = () => {
      const current = queryClient.getQueryData<CurrentUser | null>(["me"]);
      return !cancelled && current?.id === userId && isCurrentSession(generation);
    };
    const load = async () => {
      if (!stillCurrent()) return;
      if (loadingRuns.current.size > 0) {
        refreshAfterLoad.current = true;
        return;
      }
      setHistoryLoading(true);
      setHistoryError(null);
      try {
        for (const runId of ids) {
          if (cancelled || loadingRuns.current.has(runId)) {
            if (loadingRuns.current.has(runId)) refreshAfterLoad.current = true;
            continue;
          }
          loadingRuns.current.add(runId);
          try {
            for (;;) {
              const after = loadedThrough.current[runId] ?? 0;
              const data = await runInSession(
                async () =>
                  requireData(
                    await api.GET("/api/admin/jobs/{id}/runs/{run_id}/steps", {
                      params: { path: { id: jobId, run_id: runId }, query: { after_seq: after } },
                    }),
                  ),
                generation,
              );
              if (!stillCurrent()) {
                return;
              }
              const page = (data.steps ?? []).map((step) => ({
                seq: step.seq,
                ts: step.ts,
                type: step.type,
                summary: step.summary,
                payload: step.payload ?? null,
              }));
              if (page.length > 0) {
                loadedThrough.current[runId] = page[page.length - 1]?.seq ?? after;
                setSteps((previous) => {
                  let list = previous[runId] ?? [];
                  for (const step of page) {
                    list = insertStep(list, step);
                  }
                  return { ...previous, [runId]: list };
                });
              }
              if (page.length < PAGE) {
                break;
              }
            }
          } catch (error) {
            if (isStaleSession(error) || !stillCurrent()) {
              return;
            }
            setHistoryError(error);
            onError(error);
            break;
          } finally {
            loadingRuns.current.delete(runId);
          }
        }
      } finally {
        if (stillCurrent()) setHistoryLoading(false);
        // A newer snapshot or run list may have requested a refresh while
        // this request was in flight. Drain that request even when this load
        // exits early because its effect was cancelled.
        if (refreshAfterLoad.current && loadingRuns.current.size === 0) {
          refreshAfterLoad.current = false;
          queueMicrotask(() => loadHistoryRef.current?.());
        }
      }
    };
    const requestLoad = () => {
      void load();
    };
    loadHistoryRef.current = requestLoad;
    requestLoad();
    return () => {
      cancelled = true;
      if (loadHistoryRef.current === requestLoad) loadHistoryRef.current = null;
    };
  }, [enabled, runIds, jobId, generation, queryClient, userId, onError]);

  // Live SSE: snapshot/job update the cached detail, steps append to history.
  useEffect(() => {
    if (!enabled) {
      return;
    }
    let disposed = false;
    let source: EventSource | null = null;
    let attempt = 0;
    let timer: number | undefined;
    let snapshotSeen = false;

    const stillCurrent = () => {
      const current = queryClient.getQueryData<CurrentUser | null>(["me"]);
      return current?.id === userId && isCurrentSession(generation);
    };

    const applyDetail = (raw: string) => {
      if (disposed || !stillCurrent()) {
        return;
      }
      try {
        const detail = JSON.parse(raw);
        if (detail?.id === jobId) {
          queryClient.setQueryData(adminJobKey(userId, jobId), detail);
        }
      } catch {
        // partial write; the next event replaces it
      }
    };

    const applySteps = (raw: string) => {
      if (disposed || !stillCurrent()) {
        return;
      }
      try {
        const parsed = JSON.parse(raw) as { run_id: string; steps: RawSseStep[] };
        if (!parsed?.run_id || !Array.isArray(parsed.steps)) {
          return;
        }
        const runId = parsed.run_id;
        const incoming = parsed.steps.flatMap((step) => {
          const seq = step.seq ?? step.Seq;
          if (typeof seq !== "number") return [];
          return [
            {
              seq,
              ts: step.ts ?? step.Ts ?? "",
              type: step.type ?? step.Type ?? "unknown",
              summary: step.summary ?? step.Summary ?? "",
              payload: parsePayload(step.payload ?? step.Payload),
            },
          ];
        });
        setSteps((previous) => {
          let list = previous[runId] ?? [];
          for (const step of incoming) {
            list = insertStep(list, step);
          }
          return { ...previous, [runId]: list };
        });
      } catch {
        // ignore malformed packet
      }
    };

    const connect = () => {
      if (disposed || !stillCurrent()) {
        return;
      }
      source = new EventSource(adminJobStreamUrl(jobId));
      source.addEventListener("open", () => setConnected(true));
      source.addEventListener("snapshot", (ev) => {
        applyDetail((ev as MessageEvent).data);
        if (snapshotSeen) {
          // A reconnect snapshot can follow a gap. The scheduler lets an
          // in-flight HTTP page finish, then starts a fresh per-run backfill.
          loadHistoryRef.current?.();
        }
        snapshotSeen = true;
      });
      source.addEventListener("job", (ev) => applyDetail((ev as MessageEvent).data));
      source.addEventListener("steps", (ev) => applySteps((ev as MessageEvent).data));
      source.onerror = () => {
        source?.close();
        source = null;
        setConnected(false);
        if (disposed) {
          return;
        }
        // EventSource hides its HTTP status; the detail request identifies a
        // current 401 so the admin session expires through the normal handler.
        void query.refetch();
        const delay = [1000, 2000, 5000, 10000][Math.min(attempt, 3)];
        attempt += 1;
        if (timer !== undefined) {
          window.clearTimeout(timer);
        }
        timer = window.setTimeout(connect, delay);
      };
    };

    connect();
    return () => {
      disposed = true;
      if (timer !== undefined) {
        window.clearTimeout(timer);
      }
      source?.close();
    };
  }, [enabled, jobId, userId, generation, queryClient, query.refetch]);

  return {
    detail: query.data,
    isPending: query.isPending,
    isError: query.isError,
    error: query.error,
    refetch: query.refetch,
    steps,
    connected,
    historyError,
    historyLoading,
    retryHistory: () => loadHistoryRef.current?.(),
  };
}
