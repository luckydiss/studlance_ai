import { api, formatDate, runInSession, useCurrentUser } from "@studlance/shared";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { requireData } from "../api";
import { adminWorkersKey } from "../session";

// /admin/workers (08-web-admin.md): name, online dot ("был N назад"),
// capabilities, codex/claude versions from info, and the current job link.

type WorkerStatus = {
  id: string;
  name: string;
  capabilities: string[];
  info: Record<string, unknown>;
  last_seen_at?: string | null;
  online: boolean;
  current_job?: { id: string; title: string } | null;
};

function lastSeenLabel(worker: WorkerStatus): string {
  if (worker.online) {
    return "сейчас";
  }
  if (!worker.last_seen_at) {
    return "нет данных";
  }
  const diff = Date.now() - new Date(worker.last_seen_at).getTime();
  const minutes = Math.max(0, Math.round(diff / 60_000));
  if (minutes < 1) {
    return "только что";
  }
  if (minutes < 60) {
    return `был ${minutes} мин назад`;
  }
  const hours = Math.round(minutes / 60);
  if (hours < 24) {
    return `был ${hours} ч назад`;
  }
  return formatDate(worker.last_seen_at);
}

function cliVersion(info: Record<string, unknown>, key: string): string | null {
  const value = info[key];
  return typeof value === "string" && value ? value : null;
}

export function WorkersPage() {
  const { data: user } = useCurrentUser();
  const query = useQuery({
    queryKey: adminWorkersKey(user?.id),
    enabled: user?.role === "admin",
    refetchInterval: 10_000,
    queryFn: async () =>
      runInSession(
        async () => requireData(await api.GET("/api/admin/workers")) as unknown as WorkerStatus[],
      ),
  });

  return (
    <main className="sl-admin-page">
      <div className="sl-admin-page-head">
        <h1>Воркеры</h1>
        {query.data && <span className="sl-admin-count">{query.data.length}</span>}
      </div>
      {query.isPending ? (
        <output className="sl-admin-state">Загружаем…</output>
      ) : query.isError ? (
        <div className="sl-admin-state">
          <p>Не удалось загрузить воркеров</p>
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
      ) : query.data.length === 0 ? (
        <p className="sl-admin-state">Воркеров нет</p>
      ) : (
        <div className="sl-admin-table-wrap">
          <table className="sl-admin-table">
            <thead>
              <tr>
                <th>Имя</th>
                <th>Статус</th>
                <th>Возможности</th>
                <th>codex</th>
                <th>claude</th>
                <th>Текущий заказ</th>
              </tr>
            </thead>
            <tbody>
              {query.data.map((worker) => {
                const codex = cliVersion(worker.info, "codex");
                const claude = cliVersion(worker.info, "claude");
                return (
                  <tr key={worker.id}>
                    <td>{worker.name}</td>
                    <td>
                      <span
                        className={`sl-admin-online${worker.online ? "" : " sl-admin-online--off"}`}
                        aria-hidden="true"
                      />
                      {lastSeenLabel(worker)}
                    </td>
                    <td>
                      {worker.capabilities.length > 0 ? (
                        <span className="sl-admin-chips">
                          {worker.capabilities.map((cap) => (
                            <span className="sl-admin-chip" key={cap}>
                              {cap}
                            </span>
                          ))}
                        </span>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td>{codex ?? "—"}</td>
                    <td>{claude ?? "—"}</td>
                    <td>
                      {worker.current_job ? (
                        <Link className="sl-admin-job-link" to={`/jobs/${worker.current_job.id}`}>
                          {worker.current_job.title || worker.current_job.id}
                        </Link>
                      ) : (
                        "—"
                      )}
                    </td>
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
