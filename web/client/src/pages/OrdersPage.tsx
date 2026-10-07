import {
  ApiError,
  type JobSummary,
  api,
  apiErrorMessage,
  formatDate,
  jobsListQueryKey,
  runInSession,
  useCurrentUser,
  useToast,
} from "@studlance/shared";
import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { Link } from "react-router-dom";
import styles from "./home/Pages.module.css";

function statusColor(status: JobSummary["client_status"]) {
  if (status === "done") return "#2B8A3E";
  if (status === "needs_input") return "#B35C00";
  if (status === "delayed") return "#686C73";
  return "#1F3A93";
}

export function OrdersPage() {
  const { show } = useToast();
  // The list cache is bound to the current user (07-web-client.md).
  const { data: user } = useCurrentUser();
  const orders = useQuery({
    queryKey: jobsListQueryKey(user?.id),
    enabled: user !== null && user !== undefined,
    // Errors are tagged with the session generation captured at request
    // start: a late failure of an old session is ignored centrally.
    queryFn: () =>
      runInSession(async () => {
        const { data, error, response } = await api.GET("/api/client/jobs");
        if (error) throw new ApiError(response.status, error.error.code, error.error.message);
        return [...data.jobs].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
      }),
    retry: false,
  });
  useEffect(() => {
    if (!orders.error) return;
    // 401 ends the session centrally (main.tsx) and the auth gate opens the
    // login form with this route as next; other errors stay on the page.
    if (!(orders.error instanceof ApiError && orders.error.status === 401)) {
      show(apiErrorMessage(orders.error));
    }
  }, [orders.error, show]);
  return (
    <main className={styles.ordersPage}>
      <div className={styles.ordersHeading}>
        <h1>Мои заказы</h1>
        <Link className={styles.primary} to="/">
          Новый заказ
        </Link>
      </div>
      {orders.isPending && <output className={styles.message}>Загружаем…</output>}
      {orders.isError && (
        <div className={styles.message} role="alert">
          <p>{apiErrorMessage(orders.error)}</p>
          <button className={styles.primary} type="button" onClick={() => void orders.refetch()}>
            Повторить
          </button>
        </div>
      )}
      {orders.data?.length === 0 && (
        <div className={styles.message}>
          <p>Заказов пока нет</p>
          <Link className={styles.primary} to="/">
            Новый заказ
          </Link>
        </div>
      )}
      {orders.data && orders.data.length > 0 && (
        <ul className={styles.orderList}>
          {orders.data.map((job) => (
            <li key={job.id}>
              <Link className={styles.orderRow} to={`/orders/${job.id}`}>
                <div className={styles.orderInfo}>
                  <span className={styles.orderTitle} title={job.title}>
                    {job.title}
                  </span>
                  <span className={styles.orderMeta}>
                    {formatDate(job.created_at)}
                    {job.current_version >= 1 && ` · версия ${job.current_version}`}
                  </span>
                </div>
                <span
                  className={styles.orderStatus}
                  style={{ color: statusColor(job.client_status) }}
                >
                  {job.status_text}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </main>
  );
}
