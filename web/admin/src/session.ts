import {
  type CurrentUser,
  apiErrorStatus,
  expireSession,
  isStaleSession,
  useCurrentUser,
} from "@studlance/shared";
import type { QueryClient } from "@tanstack/react-query";

/** A mutation completed after its card route was replaced. */
export class StaleAdminViewError extends Error {
  readonly cause: unknown;

  constructor(cause: unknown) {
    super("admin card view changed while request was pending");
    this.name = "StaleAdminViewError";
    this.cause = cause;
  }
}

export function isStaleAdminView(error: unknown): error is StaleAdminViewError {
  return error instanceof StaleAdminViewError;
}

// Admin session handling (08-web-admin.md). The panel is a separate SPA from
// the cabinet: it has its own QueryClient, its own cache keys and its own
// module state, so nothing of the cabinet leaks in or out. The server gates
// /admin/* by role, but the UI also refuses to render private screens until
// `me.role === admin` is confirmed, and a 401 of the current session ends the
// local session before any private screen or request continues.

/** Admin cache keys are user-scoped and separate from the cabinet keys. */
export function adminJobsKey(userId: string | null | undefined) {
  return ["admin-jobs", userId] as const;
}

export function adminJobKey(userId: string | null | undefined, id: string) {
  return ["admin-job", userId, id] as const;
}

export function adminClientsKey(userId: string | null | undefined) {
  return ["admin-clients", userId] as const;
}

export function adminWorkersKey(userId: string | null | undefined) {
  return ["admin-workers", userId] as const;
}

export function adminRunStepsKey(userId: string | null | undefined, jobId: string, runId: string) {
  return ["admin-steps", userId, jobId, runId] as const;
}

/** True when the current user is a confirmed admin. */
export function isAdmin(user: CurrentUser | null | undefined): boolean {
  return user?.role === "admin";
}

/**
 * Builds the central cache-error handler for the admin QueryClient. A 401 of
 * the current session instance ends the local session (me → null, private
 * admin caches and queries dropped); late errors of an old instance are
 * ignored. A plain network error stays on the page for a retry.
 */
export function makeAdminErrorHandler(getClient: () => QueryClient) {
  return (error: unknown) => {
    if (isStaleSession(error) || isStaleAdminView(error)) {
      return;
    }
    if (apiErrorStatus(error) === 401) {
      expireAdminSession(getClient());
      redirectToLogin();
    }
  };
}

/**
 * Ends the admin session locally: the shared expiry clears `me` and cancels
 * in-flight requests; the admin caches (which the shared helper does not know
 * about) are dropped here too, so no private admin data survives.
 */
export function expireAdminSession(queryClient: QueryClient) {
  expireSession(queryClient);
  queryClient.removeQueries({ queryKey: ["admin-jobs"] });
  queryClient.removeQueries({ queryKey: ["admin-job"] });
  queryClient.removeQueries({ queryKey: ["admin-clients"] });
  queryClient.removeQueries({ queryKey: ["admin-workers"] });
  queryClient.removeQueries({ queryKey: ["admin-steps"] });
}

/**
 * Sends the browser to the shared login form, remembering the current admin
 * address as the local return path. A full navigation is used because the
 * login form lives in the cabinet SPA (08-web-admin.md).
 */
export function redirectToLogin() {
  const here = window.location.pathname + window.location.search;
  // The module session marker is lost when navigation enters the cabinet SPA.
  // Carry an explicit reauthentication request across that boundary so a
  // successful /me response from a still-live cookie cannot skip the form.
  window.location.replace(`/login?next=${encodeURIComponent(here)}&reauth=1`);
}

export { useCurrentUser };
