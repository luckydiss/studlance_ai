import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@studlance/shared/theme.css";
import { ToastProvider, apiErrorStatus, expireSession } from "@studlance/shared";
import { App } from "./App";

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("root element not found");
}

// 401 from any client query or mutation ends the local session centrally:
// me becomes null (so a stale cached 200 cannot push the user off the login
// form), private caches are dropped and in-flight requests cancelled. The
// auth gate then routes to /login with the current path as `next`. Plain
// network errors stay on the page with their retry UI.
const onCacheError = (error: unknown) => {
  if (apiErrorStatus(error) === 401) {
    expireSession(queryClient);
  }
};

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError: onCacheError }),
  mutationCache: new MutationCache({ onError: onCacheError }),
  defaultOptions: {
    queries: {
      retry: 1,
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
