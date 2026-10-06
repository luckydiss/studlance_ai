import {
  type JobDetail,
  api,
  apiErrorStatus,
  bundleUrl,
  formatDate,
  formatOrderDate,
  jobQueryKey,
  setJobCache,
  useJobStream,
} from "@studlance/shared";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import styles from "../job/Job.module.css";
import { KitViewer } from "../job/KitViewer";
import { StatusWindow } from "../job/StatusWindow";
import { Uploading } from "../job/Uploading";
import { requireData, useInvalidateJob, useJobError } from "../job/api";

export function JobPage() {
  const { id = "" } = useParams();
  const onError = useJobError();
  const query = useQuery({
    queryKey: jobQueryKey(id),
    queryFn: async () =>
      requireData(await api.GET("/api/client/jobs/{id}", { params: { path: { id } } })),
    retry: (count, error) =>
      apiErrorStatus(error) !== 404 && apiErrorStatus(error) !== 401 && count < 2,
  });
  useJobStream(id, true);
  useEffect(() => {
    if (query.error && apiErrorStatus(query.error) !== 404) onError(query.error);
  }, [query.error, onError]);
  if (query.data) return <JobContent key={id} detail={query.data} />;
  if (query.isPending) {
    return (
      <main className={styles.empty}>
        <output>Загружаем…</output>
      </main>
    );
  }
  if (apiErrorStatus(query.error) === 404) {
    return (
      <main className={styles.empty}>
        <h1>Заказ не найден</h1>
        <Link to="/orders">Мои заказы</Link>
      </main>
    );
  }
  return (
    <main className={styles.empty}>
      <p>Не удалось загрузить заказ</p>
      <button
        type="button"
        className={styles.secondary}
        onClick={() => {
          void query.refetch();
        }}
      >
        Повторить
      </button>
      <Link to="/orders">Мои заказы</Link>
    </main>
  );
}

function JobContent({ detail }: { detail: JobDetail }) {
  const [confirming, setConfirming] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  const question = useRef<HTMLElement>(null);
  const [answer, setAnswer] = useState("");
  const client = useQueryClient();
  const invalidate = useInvalidateJob(detail.id);
  const onError = useJobError();
  const cancel = useMutation({
    mutationFn: async () =>
      requireData(
        await api.POST("/api/client/jobs/{id}/cancel", { params: { path: { id: detail.id } } }),
      ),
    onSuccess: (data) => {
      setConfirming(false);
      setJobCache(client, detail.id, data);
      void invalidate();
    },
    onError,
  });
  const reply = useMutation({
    mutationFn: async () =>
      requireData(
        await api.POST("/api/client/jobs/{id}/answer", {
          params: { path: { id: detail.id } },
          body: { text: answer.trim() },
        }),
      ),
    onSuccess: (data) => {
      setAnswer("");
      setJobCache(client, detail.id, data);
      void invalidate();
    },
    onError,
  });
  useEffect(() => {
    if (confirming) dialog.current?.showModal();
    else dialog.current?.close();
  }, [confirming]);
  const kit =
    detail.current_version > 0 &&
    detail.client_status !== "delayed" &&
    detail.client_status !== "needs_input";
  return (
    <div className={styles.page}>
      <header className={styles.orderHeader}>
        <div>
          <Link className={styles.backLink} to="/orders">
            ← Мои заказы
          </Link>
          <div className={styles.orderDate}>{formatOrderDate(detail.created_at)}</div>
          <h1 className={styles.orderTitle}>{detail.title}</h1>
          <p className={styles.muted}>
            {detail.status_text} · {formatDate(detail.updated_at)}
          </p>
        </div>
        <div className={styles.actions}>
          {detail.current_version > 0 && (
            <a className={styles.primary} href={bundleUrl(detail.id, detail.current_version)}>
              <svg
                aria-hidden="true"
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <path d="M12 4v11" />
                <path d="m7 10 5 5 5-5" />
                <path d="M5 20h14" />
              </svg>
              Скачать всё
            </a>
          )}
          {detail.can_cancel && (
            <button
              type="button"
              className={styles.secondary}
              onClick={() => {
                setConfirming(true);
              }}
            >
              Отменить заказ
            </button>
          )}
        </div>
      </header>
      <main className={styles.desk}>
        {detail.client_status === "canceled" ? (
          <div className={styles.deskPad}>
            <div className={styles.empty}>
              <h2>Заказ отменён</h2>
              <Link to="/">Новый заказ</Link>
            </div>
          </div>
        ) : detail.client_status === "uploading" ? (
          <div className={styles.deskPad}>
            <Uploading detail={detail} />
          </div>
        ) : kit ? (
          <KitViewer detail={detail} />
        ) : (
          <div className={styles.deskPad}>
            {detail.client_status === "needs_input" && (
              <section
                className={styles.questionCard}
                ref={question}
                tabIndex={-1}
                aria-labelledby="question-title"
              >
                <h2 id="question-title">Нужно уточнение</h2>
                <p>{detail.question}</p>
                {detail.can_answer && (
                  <form
                    onSubmit={(event) => {
                      event.preventDefault();
                      if (answer.trim() && !reply.isPending) reply.mutate();
                    }}
                  >
                    <label htmlFor="job-answer">Ваш ответ</label>
                    <textarea
                      id="job-answer"
                      placeholder="Ваш ответ"
                      maxLength={10000}
                      value={answer}
                      disabled={reply.isPending}
                      onChange={(event) => {
                        setAnswer(event.target.value);
                      }}
                    />
                    <button
                      type="submit"
                      className={styles.primary}
                      disabled={!answer.trim() || reply.isPending}
                    >
                      Ответить
                    </button>
                  </form>
                )}
              </section>
            )}
            {detail.client_status === "delayed" && (
              <p className={styles.delay}>
                Задерживается — мы уже разбираемся. Обычно это занимает до часа.
              </p>
            )}
            <PlannedSheets detail={detail} />
          </div>
        )}
      </main>
      <StatusWindow
        detail={detail}
        onAnswer={() => {
          question.current?.scrollIntoView({ behavior: "smooth", block: "center" });
          question.current?.focus({ preventScroll: true });
        }}
      />
      <dialog
        ref={dialog}
        className={styles.dialog}
        aria-labelledby="cancel-title"
        onCancel={() => {
          setConfirming(false);
        }}
        onClose={() => {
          setConfirming(false);
        }}
      >
        <h2 id="cancel-title">Отменить заказ? Работа будет остановлена.</h2>
        <div className={styles.actions}>
          <button
            type="button"
            className={styles.primary}
            disabled={cancel.isPending}
            onClick={() => cancel.mutate()}
          >
            Отменить
          </button>
          <button
            type="button"
            className={styles.secondary}
            onClick={() => {
              setConfirming(false);
            }}
          >
            Не отменять
          </button>
        </div>
      </dialog>
    </div>
  );
}

function PlannedSheets({ detail }: { detail: JobDetail }) {
  const planned = detail.planned_documents;
  if (planned.length === 0) {
    return (
      <div className={styles.planned}>
        {[1, 2, 3].map((number) => (
          <figure key={number}>
            <div className={`${styles.plannedSheet} sl-planned-sheet--pulse`} />
            <figcaption>Готовим комплект</figcaption>
          </figure>
        ))}
      </div>
    );
  }
  return (
    <div className={styles.planned}>
      {planned.map((document, index) => (
        <figure key={`${index}-${document.title}`}>
          <div className={styles.plannedSheet}>
            <strong>{document.title}</strong>
            <span className={styles.muted}>{document.kind}</span>
          </div>
          <figcaption>
            <strong>{document.title}</strong>
            <div className={styles.muted}>{document.kind}</div>
          </figcaption>
        </figure>
      ))}
    </div>
  );
}
