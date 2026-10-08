import { useCurrentUser } from "@studlance/shared";
import { type ComponentType, type ReactNode, useEffect } from "react";
import { Outlet } from "react-router-dom";
import { redirectToLogin } from "./session";

// Admin auth gate (08-web-admin.md): private screens and admin requests are
// not started until `me.role === admin` is confirmed. The server already
// gates /admin/*, but the UI must not trust that alone. A confirmed client
// (or an anonymous visitor) is sent to the shared login form; the server
// redirect already carries the panel address as `next`.

export function AdminAuth({ layout: Layout }: { layout: ComponentType<{ children: ReactNode }> }) {
  const { data: user, isPending, isError, refetch } = useCurrentUser();
  const confirmed = !isPending && !isError && user?.role === "admin";

  useEffect(() => {
    if (isPending || isError) {
      return;
    }
    if (!user) {
      // No session (or a failed check): the login form lives in the cabinet.
      redirectToLogin();
      return;
    }
    if (user.role !== "admin") {
      // A signed-in client must not see the panel. The cabinet's login page
      // returns them to the cabinet; there is no loop back into /admin.
      window.location.replace("/login");
    }
  }, [isPending, isError, user]);

  if (isPending) {
    return <output className="sl-loading">Загружаем…</output>;
  }
  if (isError) {
    return (
      <main className="sl-admin-page sl-admin-state">
        <p>Не удалось проверить вход</p>
        <button type="button" className="sl-admin-button" onClick={() => void refetch()}>
          Повторить
        </button>
      </main>
    );
  }
  if (!confirmed) {
    // Never render private screens before the role is confirmed.
    return null;
  }
  return (
    <Layout>
      <Outlet />
    </Layout>
  );
}
