import { api, formatOrderDate, runInSession, useCurrentUser } from "@studlance/shared";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { requireData } from "../api";
import { adminClientsKey } from "../session";

// /admin/clients (08-web-admin.md): email, name, total orders, last order.
// A click opens the orders screen filtered by this exact client id.

type ClientAccount = {
  id: string;
  email: string;
  name: string;
  jobs_total: number;
  last_job_at?: string | null;
};

export function ClientsPage() {
  const { data: user } = useCurrentUser();
  const query = useQuery({
    queryKey: adminClientsKey(user?.id),
    enabled: user?.role === "admin",
    queryFn: async () =>
      runInSession(
        async () => requireData(await api.GET("/api/admin/clients")) as unknown as ClientAccount[],
      ),
  });

  return (
    <main className="sl-admin-page">
      <div className="sl-admin-page-head">
        <h1>Клиенты</h1>
        {query.data && <span className="sl-admin-count">{query.data.length}</span>}
      </div>
      {query.isPending ? (
        <output className="sl-admin-state">Загружаем…</output>
      ) : query.isError ? (
        <div className="sl-admin-state">
          <p>Не удалось загрузить клиентов</p>
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
        <p className="sl-admin-state">Клиентов нет</p>
      ) : (
        <div className="sl-admin-table-wrap">
          <table className="sl-admin-table">
            <thead>
              <tr>
                <th>Почта</th>
                <th>Имя</th>
                <th>Заказов</th>
                <th>Последний заказ</th>
              </tr>
            </thead>
            <tbody>
              {query.data.map((client) => (
                <tr key={client.id}>
                  <td>
                    <Link className="sl-admin-job-link" to={`/jobs?client=${client.id}`}>
                      {client.email}
                    </Link>
                  </td>
                  <td>{client.name}</td>
                  <td>{client.jobs_total}</td>
                  <td>{client.last_job_at ? formatOrderDate(client.last_job_at) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </main>
  );
}
