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

// The session instance generation: bumped whenever a session ends locally
// (a client-API 401), on logout and at the start of every login — including
// a re-login of the same user. Requests, mutations and subscriptions capture
// the generation when they START; a callback (success, error, progress)
// carrying an old generation must be ignored before any side effect
// (cache writes, invalidation, navigation, session expiry, toasts, form
// changes). Two logins of the same user are different session instances.
let generation = 0;

/** The current session instance generation. */
export function sessionGeneration() {
  return generation;
}

export function isCurrentSession(value: number) {
  return value === generation;
}

/**
 * Attaches the captured generation to a thrown error so central and local
 * error handlers can ignore results of an old session instance.
 */
export function tagSessionError(error: unknown, value: number): unknown {
  if (error instanceof Error) {
    (error as Error & { sessionGeneration?: number }).sessionGeneration = value;
  }
  return error;
}

/**
 * True when the error was thrown by a request of a session instance that is
 * no longer current. Untagged errors (e.g. thrown synchronously by the
 * client itself) are never considered stale.
 */
export function isStaleSession(error: unknown) {
  const value = (error as { sessionGeneration?: number } | null)?.sessionGeneration;
  return typeof value === "number" && !isCurrentSession(value);
}

/**
 * Runs a request inside the current session instance: thrown errors are
 * tagged with the generation captured at the start, so late failures of an
 * old session cannot expire or redirect a newer one.
 */
export async function runInSession<T>(request: () => Promise<T>, value?: number): Promise<T> {
  const captured = value ?? sessionGeneration();
  try {
    return await request();
  } catch (error) {
    throw tagSessionError(error, captured);
  }
}

// Set when a client-API 401 ended the session locally: the login form must
// trust this state instead of re-verifying me (the server may still answer
// /me 200 for other routes, but this session is over for the client).
let sessionExpired = false;

export function isSessionExpired() {
  return sessionExpired;
}

// The return path for the login form after a local session expiry. It is the
// single source of `next`: the 401 handler that ends the session records it
// (a created order returns to `/orders/{id}`), and the auth gate reads it
// instead of the current route, so two redirects can never race and no React
// or Query ordering can replace the recorded path with the original `/`.
// Cleared on every login and logout (a new session starts clean).
let expiredReturnPath: string | null = null;

/** The `next` recorded by the latest local session expiry, or null. */
export function getExpiredReturnPath() {
  return expiredReturnPath;
}

/**
 * Ends the local session without touching the server: the cached `me`
 * becomes null (fresh, so a stale 200 can no longer push the user off the
 * login form), in-flight private queries are cancelled and their caches
 * dropped. Late responses and SSE events of the old session cannot put
 * private data back: job keys carry the user id, a null `me` gates every
 * write of the old session, and the generation bump fences its callbacks.
 * `returnPath` (when known) is remembered for the auth gate.
 */
export function expireSession(queryClient: QueryClient, returnPath?: string) {
  generation += 1;
  sessionExpired = true;
  expiredReturnPath = returnPath ?? null;
  void queryClient.cancelQueries();
  queryClient.setQueryData<CurrentUser | null>(["me"], null);
  queryClient.removeQueries({ queryKey: ["job"] });
  queryClient.removeQueries({ queryKey: ["jobs"] });
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
      // A new session instance starts clean: everything of the previous one
      // (responses in flight, caches, live data) is fenced off and dropped
      // before the new session renders anything.
      generation += 1;
      sessionExpired = false;
      expiredReturnPath = null;
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
      generation += 1;
      sessionExpired = false;
      expiredReturnPath = null;
      void queryClient.cancelQueries();
      queryClient.clear();
    },
  });
}
