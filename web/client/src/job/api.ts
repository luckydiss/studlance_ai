import {
  apiErrorMessage,
  apiErrorStatus,
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
  return useCallback(
    (error: unknown) => {
      toast(error);
      if (apiErrorStatus(error) === 401) {
        navigate(`/login?next=${encodeURIComponent(location.pathname + location.search)}`, {
          replace: true,
        });
      }
    },
    [toast, navigate, location.pathname, location.search],
  );
}

export function useInvalidateJob(id: string) {
  const client = useQueryClient();
  return useCallback(
    () =>
      Promise.all([
        client.invalidateQueries({ queryKey: jobQueryKey(id) }),
        client.invalidateQueries({ queryKey: jobsListQueryKey() }),
      ]),
    [client, id],
  );
}
