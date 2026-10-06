import type { QueryClient } from "@tanstack/react-query";
import type { components } from "./api/types.gen";

// Shared query keys and cache helpers for job data (07-web-client.md).
// The JobDetail cache is fed by both the REST query and the SSE stream
// (snapshot + job events both carry the full detail).

export type JobDetail = components["schemas"]["ClientJobDetail"];
export type JobSummary = components["schemas"]["JobSummary"];
export type Document = components["schemas"]["Document"];
export type Page = components["schemas"]["Page"];
export type Remark = components["schemas"]["Remark"];
export type StatusStep = components["schemas"]["StatusStep"];
export type Revision = components["schemas"]["Revision"];

export function jobQueryKey(jobId: string) {
  return ["job", jobId] as const;
}

export function jobsListQueryKey() {
  return ["jobs"] as const;
}

/** Replaces the cached job detail (used by the SSE snapshot/job events). */
export function setJobCache(queryClient: QueryClient, jobId: string, detail: JobDetail) {
  queryClient.setQueryData(jobQueryKey(jobId), detail);
  // A status change also affects the orders list.
  queryClient.invalidateQueries({ queryKey: jobsListQueryKey() });
}
