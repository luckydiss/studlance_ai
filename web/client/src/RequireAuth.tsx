import { getExpiredReturnPath, useCurrentUser } from "@studlance/shared";
import type { ComponentType, ReactNode } from "react";
import { Navigate, Outlet, useLocation } from "react-router-dom";

// Auth gate: no session → /login with a `next` return path; the path is
// validated as a local one on the login page itself (07-web-client.md).
// When a local session expiry recorded a return path (a created order), the
// gate uses it instead of the current route: the expiry and this redirect
// share one source, so no React/Query ordering can substitute the original /.

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
    const returnPath = getExpiredReturnPath() ?? location.pathname + (location.search || "");
    const next = encodeURIComponent(returnPath);
    return <Navigate to={`/login?next=${next}`} replace />;
  }
  return (
    <Layout>
      <Outlet />
    </Layout>
  );
}
