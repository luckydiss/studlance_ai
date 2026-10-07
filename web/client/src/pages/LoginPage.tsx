import { isSessionExpired, safeNextPath, useCurrentUser, useLogin } from "@studlance/shared";
import { useEffect, useState } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router-dom";
import styles from "./home/Pages.module.css";

export function LoginPage() {
  const me = useCurrentUser();
  const { data: user, isPending } = me;
  const login = useLogin();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  // The cached `me` (staleTime 60 s) must not hide the form when the session
  // is actually gone: verify it against the server once on mount. A 401
  // resolves to null and the form stays; a live session still redirects.
  const refetchMe = me.refetch;
  useEffect(() => {
    // The form must not trust a cached (possibly stale) `me` — but a session
    // ended locally by a 401 is final: no re-verification, the form stays.
    if (isSessionExpired()) {
      return;
    }
    void refetchMe();
  }, [refetchMe]);
  // The successful form submission owns the redirect to ?next=.
  if (user && !login.isSuccess) return <Navigate to="/" replace />;
  return (
    <main className={styles.loginPage}>
      <form
        className={styles.loginCard}
        onSubmit={(event) => {
          event.preventDefault();
          if (login.isPending) return;
          login.mutate(
            { email: email.trim(), password },
            {
              onSuccess: () => navigate(safeNextPath(params.get("next")), { replace: true }),
            },
          );
        }}
      >
        <h1>Вход в studlance</h1>
        <label htmlFor="email">Почта</label>
        <input
          id="email"
          type="email"
          autoComplete="username"
          required
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          disabled={login.isPending}
        />
        <label htmlFor="password">Пароль</label>
        <input
          id="password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          disabled={login.isPending}
        />
        <button className={styles.primary} type="submit" disabled={isPending || login.isPending}>
          Войти
        </button>
        {login.error && (
          <p className={styles.error} role="alert">
            {login.error.message}
          </p>
        )}
      </form>
    </main>
  );
}
