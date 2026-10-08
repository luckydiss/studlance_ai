import {
  api,
  apiErrorStatus,
  formatDate,
  isCurrentSession,
  isStaleSession,
  runInSession,
  sessionGeneration,
  useCurrentUser,
  useToast,
} from "@studlance/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useLocation, useParams } from "react-router-dom";
import { useAdminJob } from "../adminJob";
import { requireData, useAdminError } from "../api";
import { type DetailsDetail, DetailsPanel } from "../components/DetailsPanel";
import { type AdminDetail, type SheetJump, SheetsPanel } from "../components/SheetsPanel";
import { type RunView, TracePanel } from "../components/TracePanel";
import { StaleAdminViewError, adminJobKey } from "../session";
import { stageLabel, statusLabel } from "../status";

export function JobReviewPage() {
  const { id = "" } = useParams();
  const viewRef = useRef<{ id: string; token: symbol; active: boolean } | null>(null);
  if (!viewRef.current || viewRef.current.id !== id) {
    if (viewRef.current) viewRef.current.active = false;
    viewRef.current = { id, token: Symbol(id), active: true };
  }
  const viewToken = viewRef.current.token;
  const isViewCurrent = useCallback(
    (token: symbol) => viewRef.current?.token === token && viewRef.current.active,
    [],
  );
  useEffect(() => {
    const view = viewRef.current;
    if (view?.token === viewToken) view.active = true;
    return () => {
      if (view?.token === viewToken) view.active = false;
    };
  }, [viewToken]);
  const location = useLocation();
  const { data: user } = useCurrentUser();
  const {
    detail,
    isPending,
    isError,
    error,
    refetch,
    steps,
    historyError,
    historyLoading,
    retryHistory,
  } = useAdminJob(id, user?.id, viewToken, isViewCurrent);
  const [jump, setJump] = useState<SheetJump | null>(null);

  const openRemark = useCallback(
    (version: number, documentId: string, page: number) => {
      setJump((previous) => ({
        jobId: id,
        version,
        documentId,
        page,
        nonce: (previous?.nonce ?? 0) + 1,
      }));
    },
    [id],
  );

  // A page jump is scoped to the card that produced the remark link.
  // biome-ignore lint/correctness/useExhaustiveDependencies: route id is the reset trigger.
  useEffect(() => {
    setJump(null);
  }, [id]);

  if (apiErrorStatus(error) === 404) {
    return (
      <main className="sl-admin-page">
        <div className="sl-admin-state">
          <h1>Заказ не найден</h1>
          <Link to="/jobs">К заказам</Link>
        </div>
      </main>
    );
  }
  if (isPending) {
    return (
      <main className="sl-admin-page">
        <output className="sl-admin-state">Загружаем…</output>
      </main>
    );
  }
  if (isError || !detail) {
    return (
      <main className="sl-admin-page">
        <div className="sl-admin-state">
          <p>Не удалось загрузить заказ</p>
          <button type="button" className="sl-admin-button" onClick={() => void refetch()}>
            Повторить
          </button>
        </div>
      </main>
    );
  }

  const adminDetail = detail as unknown as AdminDetail &
    DetailsDetail & {
      title: string;
      status: string;
      stage?: string | null;
      client: { id: string; email: string; name: string };
      current_version: number;
      attempt: number;
      worker?: string | null;
      lease_expires_at?: string | null;
      needs_attention: boolean;
      error?: string | null;
      question?: string | null;
      updated_at?: string;
      agent_runs: RunView[];
    };

  const lease =
    adminDetail.lease_expires_at != null
      ? Math.max(
          0,
          Math.round((new Date(adminDetail.lease_expires_at).getTime() - Date.now()) / 1000),
        )
      : null;

  return (
    <main className="sl-admin-review">
      <header className="sl-admin-review-head">
        <div className="sl-admin-review-title">
          <Link className="sl-admin-back" to={`/jobs${location.search}`}>
            ← Заказы
          </Link>
          <h1>{adminDetail.title}</h1>
          <div className="sl-admin-muted">
            {adminDetail.client.name} · {adminDetail.client.email} ·{" "}
            {statusLabel(adminDetail.status)}
            {stageLabel(adminDetail.stage) ? ` · ${stageLabel(adminDetail.stage)}` : ""} · версия{" "}
            {adminDetail.current_version} · попытка {adminDetail.attempt}
            {adminDetail.worker ? ` · воркер ${adminDetail.worker}` : ""}
            {lease != null ? ` · lease ${lease} с` : ""}
          </div>
        </div>
        {adminDetail.needs_attention && (
          <span className="sl-admin-attention-flag">Требует внимания</span>
        )}
      </header>

      {adminDetail.error && <p className="sl-admin-error-row">{adminDetail.error}</p>}

      <ActionsBar
        detail={adminDetail}
        userId={user?.id}
        viewToken={viewToken}
        isViewCurrent={isViewCurrent}
      />

      <div className="sl-admin-columns">
        <TracePanel
          jobId={adminDetail.id}
          runs={adminDetail.agent_runs}
          steps={steps}
          historyError={historyError}
          historyLoading={historyLoading}
          onRetryHistory={retryHistory}
        />
        <SheetsPanel
          key={adminDetail.id}
          detail={adminDetail}
          jump={jump?.jobId === adminDetail.id ? jump : null}
        />
        <DetailsPanel
          detail={adminDetail}
          userId={user?.id}
          viewToken={viewToken}
          isViewCurrent={isViewCurrent}
          onOpenRemark={openRemark}
        />
      </div>
      <p className="sl-admin-muted sl-admin-updated">
        Обновлено {formatDate(adminDetail.updated_at ?? "")}
      </p>
    </main>
  );
}

type ReviewDetail = DetailsDetail & {
  id: string;
  status: string;
  needs_attention: boolean;
  question?: string | null;
};

function ActionsBar({
  detail,
  userId,
  viewToken,
  isViewCurrent,
}: {
  detail: ReviewDetail;
  userId: string | undefined;
  viewToken: symbol;
  isViewCurrent: (token: symbol) => boolean;
}) {
  const [answer, setAnswer] = useState("");
  const [confirmingCancel, setConfirmingCancel] = useState(false);
  const onError = useAdminError();
  const queryClient = useQueryClient();
  const { show } = useToast();
  const generation = sessionGeneration();

  // biome-ignore lint/correctness/useExhaustiveDependencies: route job id is the reset trigger.
  useEffect(() => {
    setAnswer("");
    setConfirmingCancel(false);
    reply.reset();
    retry.reset();
    cancel.reset();
    attention.reset();
  }, [detail.id]);

  const apply = useCallback(
    async (data: unknown) => {
      queryClient.setQueryData(adminJobKey(userId, detail.id), data);
      await queryClient.invalidateQueries({ queryKey: adminJobKey(userId, detail.id) });
    },
    [queryClient, userId, detail.id],
  );

  const handleError = useCallback(
    (error: unknown) => {
      if (isStaleSession(error)) {
        return;
      }
      if (apiErrorStatus(error) === 409) {
        show("Состояние заказа изменилось — обновляем карточку");
        void queryClient.invalidateQueries({ queryKey: adminJobKey(userId, detail.id) });
        return;
      }
      onError(error);
    },
    [onError, queryClient, userId, detail.id, show],
  );

  const isCurrentView = (variables: { jobId: string; viewToken: symbol; generation: number }) =>
    variables.jobId === detail.id &&
    isViewCurrent(variables.viewToken) &&
    isCurrentSession(variables.generation);
  const guarded = async <T,>(
    variables: { jobId: string; viewToken: symbol },
    request: () => Promise<T>,
  ) => {
    try {
      return await request();
    } catch (error) {
      if (variables.jobId !== detail.id || !isViewCurrent(variables.viewToken)) {
        throw new StaleAdminViewError(error);
      }
      throw error;
    }
  };

  const reply = useMutation({
    mutationFn: async (variables: {
      jobId: string;
      text: string;
      generation: number;
      viewToken: symbol;
    }) =>
      guarded(variables, async () => {
        const data = await runInSession(
          async () =>
            requireData(
              await api.POST("/api/admin/jobs/{id}/answer", {
                params: { path: { id: variables.jobId } },
                body: { text: variables.text },
              }),
            ),
          variables.generation,
        );
        return data;
      }),
    onSuccess: async (data, variables) => {
      if (!isCurrentView(variables)) {
        return;
      }
      setAnswer("");
      await apply(data);
    },
    onError: (error, variables) => {
      if (isCurrentView(variables)) handleError(error);
    },
  });

  const retry = useMutation({
    mutationFn: async (variables: {
      jobId: string;
      text: string;
      generation: number;
      viewToken: symbol;
    }) =>
      guarded(variables, () =>
        runInSession(
          async () =>
            requireData(
              await api.POST("/api/admin/jobs/{id}/retry", {
                params: { path: { id: variables.jobId } },
              }),
            ),
          variables.generation,
        ),
      ),
    onSuccess: async (data, variables) => {
      if (!isCurrentView(variables)) {
        return;
      }
      await apply(data);
    },
    onError: (error, variables) => {
      if (isCurrentView(variables)) handleError(error);
    },
  });

  const cancel = useMutation({
    mutationFn: async (variables: {
      jobId: string;
      text: string;
      generation: number;
      viewToken: symbol;
    }) =>
      guarded(variables, () =>
        runInSession(
          async () =>
            requireData(
              await api.POST("/api/admin/jobs/{id}/cancel", {
                params: { path: { id: variables.jobId } },
              }),
            ),
          variables.generation,
        ),
      ),
    onSuccess: async (data, variables) => {
      if (!isCurrentView(variables)) {
        return;
      }
      setConfirmingCancel(false);
      await apply(data);
    },
    onError: (error, variables) => {
      if (isCurrentView(variables)) handleError(error);
    },
  });

  const attention = useMutation({
    mutationFn: async (variables: {
      jobId: string;
      text: string;
      generation: number;
      viewToken: symbol;
    }) =>
      guarded(variables, () =>
        runInSession(
          async () =>
            requireData(
              await api.POST("/api/admin/jobs/{id}/attention", {
                params: { path: { id: variables.jobId } },
                body: { needs_attention: false },
              }),
            ),
          variables.generation,
        ),
      ),
    onSuccess: async (data, variables) => {
      if (!isCurrentView(variables)) {
        return;
      }
      await apply(data);
    },
    onError: (error, variables) => {
      if (isCurrentView(variables)) handleError(error);
    },
  });

  const canCancel = ["uploading", "queued", "running", "needs_input"].includes(detail.status);
  const busy = reply.isPending || retry.isPending || cancel.isPending || attention.isPending;

  return (
    <div className="sl-admin-actions">
      {detail.status === "needs_input" && (
        <form
          className="sl-admin-answer"
          onSubmit={(event) => {
            event.preventDefault();
            if (answer.trim() && !reply.isPending) {
              reply.mutate({ jobId: detail.id, text: answer.trim(), generation, viewToken });
            }
          }}
        >
          <label htmlFor="admin-answer">Ответить за клиента</label>
          <textarea
            id="admin-answer"
            value={answer}
            disabled={busy}
            onChange={(event) => {
              setAnswer(event.target.value);
            }}
          />
          <button type="submit" className="sl-admin-button" disabled={!answer.trim() || busy}>
            Ответить
          </button>
        </form>
      )}
      {detail.status === "failed" && (
        <button
          type="button"
          className="sl-admin-button"
          disabled={busy}
          onClick={() => retry.mutate({ jobId: detail.id, text: "", generation, viewToken })}
        >
          Повторить
        </button>
      )}
      {detail.needs_attention && (
        <button
          type="button"
          className="sl-admin-button sl-admin-button--ghost"
          disabled={busy}
          onClick={() => attention.mutate({ jobId: detail.id, text: "", generation, viewToken })}
        >
          Снять флаг внимания
        </button>
      )}
      {canCancel &&
        (confirmingCancel ? (
          <span className="sl-admin-cancel-confirm">
            <button
              type="button"
              className="sl-admin-button sl-admin-button--danger"
              disabled={busy}
              onClick={() => cancel.mutate({ jobId: detail.id, text: "", generation, viewToken })}
            >
              Отменить
            </button>
            <button
              type="button"
              className="sl-admin-button sl-admin-button--ghost"
              onClick={() => setConfirmingCancel(false)}
            >
              Не отменять
            </button>
          </span>
        ) : (
          <button
            type="button"
            className="sl-admin-button sl-admin-button--danger"
            disabled={busy}
            onClick={() => setConfirmingCancel(true)}
          >
            Отменить
          </button>
        ))}
    </div>
  );
}
