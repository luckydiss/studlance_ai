import type { JobDetail } from "@studlance/shared";
import { useEffect, useRef, useState } from "react";
import styles from "./Job.module.css";

export function StatusWindow({
  detail,
  onAnswer,
}: {
  detail: JobDetail;
  onAnswer: () => void;
}) {
  const key = `sl.statuswin.${detail.id}.collapsed`;
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem(key) === "true";
    } catch {
      return false;
    }
  });
  const previous = useRef(detail.client_status);
  useEffect(() => {
    if (previous.current !== detail.client_status) setCollapsed(false);
    previous.current = detail.client_status;
    if (detail.client_status === "done") {
      const timer = window.setTimeout(() => setCollapsed(true), 10000);
      return () => window.clearTimeout(timer);
    }
  }, [detail.client_status]);
  useEffect(() => {
    try {
      localStorage.setItem(key, String(collapsed));
    } catch {
      /* Storage may be disabled. */
    }
  }, [collapsed, key]);
  if (detail.client_status === "uploading" || detail.client_status === "canceled") return null;
  return (
    <section className={styles.statusWindow} aria-label="Статус заказа">
      <button
        type="button"
        className={styles.statusToggle}
        aria-expanded={!collapsed}
        aria-controls={`status-steps-${detail.id}`}
        onClick={() => setCollapsed(!collapsed)}
      >
        <span className={styles.dot} />
        <strong>
          {detail.client_status === "done"
            ? `Готово — версия ${detail.current_version}`
            : detail.status_text}
        </strong>
        <svg
          role="img"
          aria-label={collapsed ? "Развернуть" : "Свернуть"}
          width="16"
          height="16"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        >
          <path d={collapsed ? "m6 9 6 6 6-6" : "m6 15 6-6 6 6"} />
        </svg>
      </button>
      {!collapsed && (
        <div id={`status-steps-${detail.id}`} className={styles.statusBody}>
          <ol className={styles.steps}>
            {detail.status_steps.map((step, index) => (
              <li
                key={`${index}-${step.title}`}
                className={step.state === "pending" ? styles.muted : ""}
              >
                {step.state === "done" ? (
                  <svg
                    role="img"
                    aria-label="Готово"
                    width="16"
                    height="16"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="#2B8A3E"
                    strokeWidth="2"
                  >
                    <path d="m5 12 4 4L19 6" />
                  </svg>
                ) : (
                  <span
                    className={
                      step.state === "active" ? `${styles.dot} ${styles.pulse}` : styles.pendingDot
                    }
                  />
                )}
                <span>{step.title}</span>
              </li>
            ))}
          </ol>
          {detail.question && (
            <div className={styles.statusQuestion}>
              <p>{detail.question}</p>
              <button type="button" className={styles.secondary} onClick={onAnswer}>
                Ответить
              </button>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
