import { type ReactNode, createContext, useCallback, useContext, useRef, useState } from "react";
import { apiErrorMessage } from "./errors";

// Bottom-center toasts with API error messages, 4 s each (07-web-client.md).

interface ToastItem {
  id: number;
  text: string;
}

interface ToastApi {
  show: (text: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const nextId = useRef(1);

  const show = useCallback((text: string) => {
    const id = nextId.current;
    nextId.current += 1;
    setItems((prev) => [...prev, { id, text }]);
    window.setTimeout(() => {
      setItems((prev) => prev.filter((t) => t.id !== id));
    }, 4000);
  }, []);

  return (
    <ToastContext.Provider value={{ show }}>
      {children}
      {/* biome-ignore lint/a11y/useSemanticElements: a live region wrapper for stacked toasts. */}
      <div className="sl-toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className="sl-toast">
            {t.text}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) {
    throw new Error("useToast requires ToastProvider");
  }
  return ctx;
}

/** Shows the message of an API error (errors.ts) as a toast. */
export function useApiErrorToast() {
  const { show } = useToast();
  return useCallback(
    (err: unknown) => {
      show(apiErrorMessage(err));
    },
    [show],
  );
}
