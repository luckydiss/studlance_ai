import type { QueryClient } from "@tanstack/react-query";
import type { components } from "./api/types.gen";

// Shared query keys and cache helpers for job data (07-web-client.md).
// The JobDetail cache is fed by both the REST query and the SSE stream
// (snapshot + job events both carry the full detail).
//
// Private data belongs to a session: the keys carry the current user id, so
// data cached for one account is never served for another, and late writes
// from an old session land in that old user's key where nobody reads them.

export type JobDetail = components["schemas"]["ClientJobDetail"];
export type JobSummary = components["schemas"]["JobSummary"];
export type Document = components["schemas"]["Document"];
export type Page = components["schemas"]["Page"];
export type Remark = components["schemas"]["Remark"];
export type StatusStep = components["schemas"]["StatusStep"];
export type Revision = components["schemas"]["Revision"];

const ANON = "anonymous";

export function jobQueryKey(userId: string | null | undefined, jobId: string) {
  return ["job", userId ?? ANON, jobId] as const;
}

export function jobsListQueryKey(userId: string | null | undefined) {
  return ["jobs", userId ?? ANON] as const;
}

/**
 * Replaces the cached job detail (used by the SSE snapshot/job events and
 * mutation callbacks). `userId` is the session the data belongs to: writes
 * for an old session land in that session's key and never leak into the
 * current view.
 */
export function setJobCache(
  queryClient: QueryClient,
  userId: string | null | undefined,
  jobId: string,
  detail: JobDetail,
) {
  if (detail.id !== jobId) {
    return;
  }
  queryClient.setQueryData(jobQueryKey(userId, jobId), detail);
  // A status change also affects the orders list.
  queryClient.invalidateQueries({ queryKey: jobsListQueryKey(userId) });
}
