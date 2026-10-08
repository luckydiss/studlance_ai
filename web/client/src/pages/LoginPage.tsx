import {
  type CurrentUser,
  isSessionExpired,
  safeNextPath,
  useCurrentUser,
  useLogin,
} from "@studlance/shared";
import { useEffect, useState } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router-dom";
import styles from "./home/Pages.module.css";

// The admin panel is a separate SPA: a validated local `next` that points at
// /admin (or /admin/…) is followed with a full navigation, because client-side
// routing here belongs to the cabinet router. Client paths keep the normal SPA
// navigation. A client account never uses an admin `next` (08-web-admin.md).
function isAdminPath(next: string): boolean {
  const pathname = next.split(/[?#]/, 1)[0] ?? "/";
  return pathname === "/admin" || pathname.startsWith("/admin/");
}

function goToNext(user: CurrentUser, next: string) {
  const safe = safeNextPath(next);
  if (isAdminPath(safe)) {
    // Only an admin may follow an admin return path, and only with a full
    // navigation (the panel is a separate SPA). A client never enters /admin.
    if (user.role === "admin") {
      window.location.replace(safe);
      return true;
    }
    return false; // caller falls back to the cabinet root
  }
  return false;
}

function cabinetNext(user: CurrentUser, next: string) {
  const safe = safeNextPath(next);
  if (isAdminPath(safe)) {
    // A client must not be routed into the panel; land in the cabinet.
    return user.role === "admin" ? "/" : "/";
  }
  return safe;
}

export function LoginPage() {
  const me = useCurrentUser();
  const { data: user, isPending } = me;
  const login = useLogin();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const reauth = params.get("reauth") === "1";
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
  if (user && !login.isSuccess && !reauth) return <Navigate to="/" replace />;
  return (
    <main className={styles.loginPage}>
      <form
        className={styles.loginCard}
        onSubmit={(event) => {
          event.preventDefault();
          if (login.isPending) return;
          const next = params.get("next");
          login.mutate(
            { email: email.trim(), password },
            {
              onSuccess: (user) => {
                if (goToNext(user, next ?? "")) {
                  return; // full navigation to the admin panel
                }
                navigate(cabinetNext(user, next ?? ""), { replace: true });
              },
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
