import { api, formatOrderDate, runInSession, useCurrentUser } from "@studlance/shared";
import { useQuery } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { requireData } from "../api";
import { adminJobsKey } from "../session";
import {
  STATUS_GROUP_LABELS,
  STATUS_GROUP_VALUES,
  type StatusGroup,
  isStatusGroup,
  stageLabel,
  statusLabel,
} from "../status";

// /admin/jobs — the orders table (08-web-admin.md): newest first, 10 s
// refresh, joint status/attention/search filters kept in the URL, one exact
// `status` sent to the API when the group has a single value; the group
// expansion and the client filter are applied client-side.

type AdminJob = {
  id: string;
  title: string;
  client: { id: string; email: string; name: string };
  status: string;
  stage?: string | null;
  current_version: number;
  needs_attention: boolean;
  attempt: number;
  worker?: string | null;
  created_at: string;
  updated_at: string;
};

export function JobsPage() {
  const { data: user } = useCurrentUser();
  const location = useLocation();
  const [params, setParams] = useSearchParams();
  const groupParam = params.get("status");
  const group: StatusGroup = isStatusGroup(groupParam) ? groupParam : "all";
  const attention = params.get("attention") === "1";
  const q = params.get("q") ?? "";
  const clientId = params.get("client");

  const single =
    STATUS_GROUP_VALUES[group].length === 1 ? STATUS_GROUP_VALUES[group][0] : undefined;

  const query = useQuery({
    queryKey: [...adminJobsKey(user?.id), group, attention, q, clientId] as const,
    enabled: user?.role === "admin",
    refetchInterval: 10_000,
    queryFn: async () =>
      runInSession(
        async () =>
          requireData(
            await api.GET("/api/admin/jobs", {
              params: {
                query: {
                  ...(single ? { status: single } : {}),
                  ...(attention ? { attention: 1 } : {}),
                  ...(q ? { q } : {}),
                },
              },
            }),
          ).jobs as unknown as AdminJob[],
      ),
  });

  const jobs = useMemo(() => {
    const values = STATUS_GROUP_VALUES[group];
    const list = query.data ?? [];
    return list.filter((job) => {
      if (values.length > 0 && !values.includes(job.status)) {
        return false;
      }
      if (clientId && job.client.id !== clientId) {
        return false;
      }
      return true;
    });
  }, [query.data, group, clientId]);

  const update = useCallback(
    (patch: Record<string, string | null>) => {
      const next = new URLSearchParams(params);
      for (const [key, value] of Object.entries(patch)) {
        if (value === null || value === "") {
          next.delete(key);
        } else {
          next.set(key, value);
        }
      }
      setParams(next, { replace: true });
    },
    [params, setParams],
  );

  return (
    <main className="sl-admin-page">
      <div className="sl-admin-page-head">
        <h1>Заказы</h1>
        <span className="sl-admin-count">{jobs.length}</span>
      </div>

      <form className="sl-admin-filters" onSubmit={(event) => event.preventDefault()}>
        <label>
          <span>Статус</span>
          <select
            value={group}
            onChange={(event) => {
              update({ status: event.target.value === "all" ? null : event.target.value });
            }}
          >
            {(Object.keys(STATUS_GROUP_LABELS) as StatusGroup[]).map((key) => (
              <option key={key} value={key}>
                {STATUS_GROUP_LABELS[key]}
              </option>
            ))}
          </select>
        </label>
        <label className="sl-admin-check">
          <input
            type="checkbox"
            checked={attention}
            onChange={(event) => {
              update({ attention: event.target.checked ? "1" : null });
            }}
          />
          <span>Только требующие внимания</span>
        </label>
        <label className="sl-admin-search">
          <span>Поиск</span>
          <input
            type="search"
            value={q}
            placeholder="Название или почта"
            onChange={(event) => {
              update({ q: event.target.value });
            }}
          />
        </label>
        {clientId && (
          <button
            type="button"
            className="sl-admin-clear"
            onClick={() => {
              update({ client: null });
            }}
          >
            Сбросить клиента
          </button>
        )}
      </form>

      {query.isPending ? (
        <output className="sl-admin-state">Загружаем…</output>
      ) : query.isError ? (
        <div className="sl-admin-state">
          <p>Не удалось загрузить заказы</p>
          <button
            type="button"
            className="sl-admin-button"
            onClick={() => {
              void query.refetch();
            }}
          >
            Повторить
          </button>
        </div>
      ) : jobs.length === 0 ? (
        <p className="sl-admin-state">Заказов нет</p>
      ) : (
        <div className="sl-admin-table-wrap">
          <table className="sl-admin-table">
            <thead>
              <tr>
                <th>Название</th>
                <th>Клиент</th>
                <th>Статус</th>
                <th>Версия</th>
                <th>Попытка</th>
                <th>Воркер</th>
                <th>Создан</th>
                <th>Обновлён</th>
              </tr>
            </thead>
            <tbody>
              {jobs.map((job) => {
                const stage = stageLabel(job.stage);
                return (
                  <tr key={job.id} className={job.needs_attention ? "sl-admin-row--attention" : ""}>
                    <td>
                      <Link className="sl-admin-job-link" to={`/jobs/${job.id}${location.search}`}>
                        {job.needs_attention && (
                          <span className="sl-admin-dot" aria-label="Требует внимания" />
                        )}
                        {job.title}
                      </Link>
                    </td>
                    <td>{job.client.email}</td>
                    <td>
                      {statusLabel(job.status)}
                      {stage && <span className="sl-admin-muted"> · {stage}</span>}
                    </td>
                    <td>{job.current_version > 0 ? job.current_version : "—"}</td>
                    <td>{job.attempt}</td>
                    <td>{job.worker ?? "—"}</td>
                    <td>{formatOrderDate(job.created_at)}</td>
                    <td>{formatOrderDate(job.updated_at)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </main>
  );
}
