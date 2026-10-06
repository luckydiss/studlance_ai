import { useCurrentUser } from "@studlance/shared";
import type { ComponentType, ReactNode } from "react";
import { Navigate, Outlet, useLocation } from "react-router-dom";

// Auth gate: no session → /login with a `next` return path; the path is
// validated as a local one on the login page itself (07-web-client.md).

export function RequireAuth({
  layout: Layout,
}: { layout: ComponentType<{ children: ReactNode }> }) {
  const location = useLocation();
  const { data: user, isPending, isError } = useCurrentUser();

  if (isPending) {
    return (
      <>
        {/* biome-ignore lint/a11y/useSemanticElements: a transient loading live region. */}
        <div className="sl-loading" role="status">
          Загружаем…
        </div>
      </>
    );
  }
  if (isError || !user) {
    const next = encodeURIComponent(location.pathname + (location.search || ""));
    return <Navigate to={`/login?next=${next}`} replace />;
  }
  return (
    <Layout>
      <Outlet />
    </Layout>
  );
}
