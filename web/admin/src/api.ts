import { apiErrorMessage, apiErrorStatus, isStaleSession, useToast } from "@studlance/shared";
import { useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { expireAdminSession, isStaleAdminView, redirectToLogin } from "./session";

// Shared admin request helpers. requireData mirrors the cabinet: it turns an
// openapi-fetch error into a thrown error carrying the HTTP status.

export function requireData<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (result.data !== undefined && result.response.ok) {
    return result.data;
  }
  throw Object.assign(new Error(apiErrorMessage({ error: result.error })), {
    status: result.response.status,
  });
}

/**
 * Admin error sink (08-web-admin.md): a 401 of the current session ends the
 * admin session and sends the user to the login form with the panel address
 * as the return path; a late error of an old session instance is ignored; a
 * plain network error only toasts.
 */
export function useAdminError() {
  const { show } = useToast();
  const queryClient = useQueryClient();
  return useCallback(
    (error: unknown) => {
      if (isStaleSession(error) || isStaleAdminView(error)) {
        return;
      }
      if (apiErrorStatus(error) === 401) {
        expireAdminSession(queryClient);
        redirectToLogin();
        return;
      }
      show(apiErrorMessage(error));
    },
    [show, queryClient],
  );
}
