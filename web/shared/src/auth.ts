import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
      queryClient.clear();
    },
  });
}
