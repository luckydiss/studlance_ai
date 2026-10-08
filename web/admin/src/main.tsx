import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@studlance/shared/theme.css";
import "./admin.css";
import { ToastProvider } from "@studlance/shared";
import { apiErrorStatus, isStaleSession } from "@studlance/shared";
import { App } from "./App";
import { makeAdminErrorHandler } from "./session";

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("root element not found");
}

// The admin panel owns its own QueryClient (separate cache from the cabinet).
// A 401 of the current session ends the local session centrally; errors of an
// old session instance are ignored (08-web-admin.md).
const onError = makeAdminErrorHandler(() => queryClient);

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError }),
  mutationCache: new MutationCache({ onError }),
  defaultOptions: {
    queries: {
      retry: (failureCount, error) => {
        if (isStaleSession(error)) return false;
        const status = apiErrorStatus(error);
        if (status === 401 || status === 403 || status === 404) return false;
        return status === 0 || status >= 500 ? failureCount < 1 : false;
      },
      refetchOnWindowFocus: false,
    },
  },
});

createRoot(rootEl).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <App />
      </ToastProvider>
    </QueryClientProvider>
  </StrictMode>,
);
