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
import { useLocation, useNavigate } from "react-router-dom";

export function requireData<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (result.data !== undefined && result.response.ok) return result.data;
  throw Object.assign(new Error(apiErrorMessage({ error: result.error })), {
    status: result.response.status,
  });
}

export function useJobError() {
  const toast = useApiErrorToast();
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  return useCallback(
    (error: unknown) => {
      // A late error of an old session instance is ignored entirely: no
      // toast, no navigation, no session expiry (the central handler has
      // already skipped it for the same reason).
      if (isStaleSession(error)) {
        return;
      }
      if (apiErrorStatus(error) === 401) {
        // The session is gone: end it locally (me=null, private caches and
        // subscriptions dropped) and open the login form with the current
        // route to return to. A plain network error only toasts.
        expireSession(queryClient);
        navigate(`/login?next=${encodeURIComponent(location.pathname + location.search)}`, {
          replace: true,
        });
        return;
      }
      toast(error);
    },
    [toast, navigate, location.pathname, location.search, queryClient],
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
