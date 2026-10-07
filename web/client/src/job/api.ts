import {
  apiErrorMessage,
  apiErrorStatus,
  expireSession,
  isStaleSession,
  jobQueryKey,
  jobsListQueryKey,
  useApiErrorToast,
} from "@studlance/shared";
import { useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { useLocation } from "react-router-dom";

export function requireData<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (result.data !== undefined && result.response.ok) return result.data;
  throw Object.assign(new Error(apiErrorMessage({ error: result.error })), {
    status: result.response.status,
  });
}

export function useJobError() {
  const toast = useApiErrorToast();
  const location = useLocation();
  const queryClient = useQueryClient();
  return useCallback(
    (error: unknown) => {
      // A late error of an old session instance is ignored entirely: no
      // toast, no redirect, no session expiry (the central handler has
      // already skipped it for the same reason).
      if (isStaleSession(error)) {
        return;
      }
      if (apiErrorStatus(error) === 401) {
        // The session is gone: end it locally (me=null, private caches and
        // subscriptions dropped) and record the current route (the job page)
        // as the return path. The auth gate performs the single redirect
        // with it, so no other redirect can overwrite next. A plain network
        // error only toasts.
        expireSession(queryClient, location.pathname + location.search);
        return;
      }
      toast(error);
    },
    [toast, location.pathname, location.search, queryClient],
  );
}

export function useInvalidateJob(userId: string | null | undefined, id: string) {
  const client = useQueryClient();
  return useCallback(
    () =>
      Promise.all([
        client.invalidateQueries({ queryKey: jobQueryKey(userId, id) }),
        client.invalidateQueries({ queryKey: jobsListQueryKey(userId) }),
      ]),
    [client, id, userId],
  );
}
