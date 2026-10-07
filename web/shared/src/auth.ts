import { type QueryClient, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api/client";

// Session helpers (04-api.md /auth): HttpOnly cookie, no tokens in storage.
// 401 from /me means "no session" and is reported as user=null.

export interface CurrentUser {
  id: string;
  email: string;
  name: string;
  role: string;
}

interface ErrorBody {
  error?: { message?: string };
}

export function useCurrentUser() {
  return useQuery({
    queryKey: ["me"],
    queryFn: async (): Promise<CurrentUser | null> => {
      const { data, response } = await api.GET("/api/auth/me");
      if (response.status === 401) {
        return null;
      }
      if (!response.ok || !data?.user) {
        throw new Error("Не удалось проверить сессию");
      }
      return data.user;
    },
    staleTime: 60_000,
    retry: false,
  });
}

// Set when a client-API 401 ended the session locally: the login form must
// trust this state instead of re-verifying me (the server may still answer
// /me 200 for other routes, but this session is over for the client).
let sessionExpired = false;

export function isSessionExpired() {
  return sessionExpired;
}

/**
 * Ends the local session without touching the server: the cached `me`
 * becomes null (fresh, so a stale 200 can no longer push the user off the
 * login form), in-flight private queries are cancelled and their caches
 * dropped. Late responses and SSE events of the old session cannot put
 * private data back: job keys carry the user id, and a null `me` gates
 * every write of the old session.
 */
export function expireSession(queryClient: QueryClient) {
  sessionExpired = true;
  void queryClient.cancelQueries();
  queryClient.setQueryData<CurrentUser | null>(["me"], null);
  queryClient.removeQueries({ queryKey: ["job"] });
  queryClient.removeQueries({ queryKey: ["jobs"] });
}

export function useLogin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { email: string; password: string }): Promise<CurrentUser> => {
      const { data, response } = await api.POST("/api/auth/login", { body: input });
      if (!response.ok || !data?.user) {
        const body = data as ErrorBody | undefined;
        throw new Error(body?.error?.message ?? "Неверная почта или пароль");
      }
      return data.user;
    },
    onSuccess: (user) => {
      // A new session starts clean: cancel everything of the previous one
      // (responses in flight, caches, live data) before showing it.
      sessionExpired = false;
      void queryClient.cancelQueries();
      queryClient.clear();
      queryClient.setQueryData(["me"], user);
    },
  });
}

export function useLogout() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (): Promise<void> => {
      const { response } = await api.POST("/api/auth/logout");
      if (!response.ok) {
        // A failed logout still clears the local session state.
        throw new Error("Не удалось выйти");
      }
    },
    onSettled: () => {
      // Drop all user data and live caches: subscriptions close with their
      // components, the query cache is cleared outright.
      sessionExpired = false;
      void queryClient.cancelQueries();
      queryClient.clear();
    },
  });
}
