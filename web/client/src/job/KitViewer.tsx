import {
  type Document,
  type JobDetail,
  type Remark,
  api,
  setJobCache,
  throwApiError,
  useCurrentUser,
  useToast,
} from "@studlance/shared";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { type PickedFile, exceedsLimit, mergeFiles } from "../files";
import type { Rect } from "../remarkMath";
import { FilePicker } from "./FilePicker";
import styles from "./Job.module.css";
import { PageCanvas } from "./PageCanvas";
import { requireData, useInvalidateJob, useJobError } from "./api";

function usePages(id: string, version: number, document: Document | undefined) {
  const onError = useJobError();
  const query = useQuery({
    queryKey: ["pages", id, version, document?.id],
    enabled: !!document && document.page_count > 0,
    staleTime: Number.POSITIVE_INFINITY,
    queryFn: async () => {
      if (!document) throw new Error("Документ не найден");
      const data = requireData(
        await api.GET("/api/client/jobs/{id}/versions/{v}/documents/{document_id}/pages", {
          params: { path: { id, v: version, document_id: document.id } },
        }),
      );
      return [...data.pages].sort((a, b) => a.page - b.page);
    },
  });
  useEffect(() => {
    if (query.error) onError(query.error);
  }, [query.error, onError]);
  return query;
}

function DocumentItem({
  id,
  version,
  document,
  selected,
  onSelect,
}: {
  id: string;
  version: number;
  document: Document;
  selected: boolean;
  onSelect: () => void;
}) {
  const query = usePages(id, version, document);
  const first = query.data?.[0];
  return (
    <div className={`${styles.documentItem} ${selected ? styles.selected : ""}`}>
      <button
        type="button"
        className={styles.documentButton}
        aria-pressed={selected}
        onClick={onSelect}
      >
        {first ? (
          <img
            className={styles.documentThumb}
            src={first.thumb_url}
            alt={`Первая страница: ${document.title}`}
          />
        ) : (
          <span className={styles.blankThumb} />
        )}
        <strong>{document.title}</strong>
        <span className={styles.muted}>
          {document.kind} · {document.page_count} стр.
        </span>
      </button>
      <a href={document.download_url}>Скачать</a>
    </div>
  );
}

export function KitViewer({ detail }: { detail: JobDetail }) {
  const [version, setVersion] = useState(detail.current_version);
  const previousLatest = useRef(detail.current_version);
  const [documentId, setDocumentId] = useState("");
  const [pageNumber, setPageNumber] = useState(1);
  const [mode, setMode] = useState(false);
  const [draftVersion, setDraftVersion] = useState(detail.current_version);
  const [remarks, setRemarks] = useState<Remark[]>([]);
  const nextNumber = useRef(1);
  const [comment, setComment] = useState("");
  const [files, setFiles] = useState<PickedFile[]>([]);
  const [readingFiles, setReadingFiles] = useState(false);
  const client = useQueryClient();
  const { data: sessionUser } = useCurrentUser();
  const invalidate = useInvalidateJob(sessionUser?.id, detail.id);
  const onError = useJobError();
  const { show } = useToast();
  useEffect(() => {
    if (previousLatest.current !== detail.current_version) {
      setVersion((selected) =>
        selected === previousLatest.current ? detail.current_version : selected,
      );
      previousLatest.current = detail.current_version;
      setMode(false);
    }
  }, [detail.current_version]);
  const selectedVersion = detail.versions.find((item) => item.version === version);
  const documents = selectedVersion?.documents ?? [];
  const selectedDocument = documents.find((item) => item.id === documentId) ?? documents[0];
  const pagesQuery = usePages(detail.id, version, selectedDocument);
  const pages = pagesQuery.data ?? [];
  const page = pages.find((item) => item.page === pageNumber) ?? pages[0];
  const pageIndex = page ? pages.indexOf(page) : -1;
  const latest = version === detail.current_version;
  const canRevise = detail.can_revise && latest;
  const editing = canRevise && mode && draftVersion === version;
  const history = detail.revisions.flatMap((revision) =>
    revision.remarks
      .filter((remark) => documents.some((document) => document.id === remark.document_id))
      .map((remark) => ({ key: `${revision.version}-${remark.idx}`, remark, draft: false })),
  );
  const drafts =
    draftVersion === version
      ? remarks.map((remark) => ({ key: `draft-${remark.idx}`, remark, draft: true }))
      : [];
  const visibleRemarks = [...history, ...drafts];
  const revision = useMutation({
    mutationFn: async () => {
      const form = new FormData();
      form.append(
        "data",
        JSON.stringify({
          comment: comment.trim(),
          remarks: remarks.map(({ idx: _idx, ...remark }) => remark),
        }),
      );
      for (const file of files) form.append("files", file.file, file.path);
      const response = await fetch(`/api/client/jobs/${encodeURIComponent(detail.id)}/revisions`, {
        method: "POST",
        credentials: "same-origin",
        body: form,
      });
      await throwApiError(response);
      return (await response.json()) as JobDetail;
    },
    onSuccess: (data) => {
      setJobCache(client, sessionUser?.id, detail.id, data);
      setMode(false);
      setRemarks([]);
      setComment("");
      setFiles([]);
      nextNumber.current = 1;
      void invalidate();
    },
    onError,
  });

  function selectVersion(value: number) {
    setVersion(value);
    setDocumentId("");
    setPageNumber(1);
  }
  function selectDocument(value: string, page = 1) {
    setDocumentId(value);
    setPageNumber(page);
  }
  useEffect(() => {
    const navigate = (event: KeyboardEvent) => {
      const target = event.target;
      if (
        target instanceof HTMLElement &&
        (target.closest("input, textarea, select, button, a, dialog, [contenteditable=true]") ||
          target.isContentEditable)
      ) {
        return;
      }
      if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      if (event.key === "ArrowLeft" && pageIndex > 0) {
        event.preventDefault();
        const previous = pages[pageIndex - 1];
        if (previous) setPageNumber(previous.page);
      }
      if (event.key === "ArrowRight" && pageIndex >= 0 && pageIndex < pages.length - 1) {
        event.preventDefault();
        const next = pages[pageIndex + 1];
        if (next) setPageNumber(next.page);
      }
    };
    window.addEventListener("keydown", navigate);
    return () => window.removeEventListener("keydown", navigate);
  }, [pages, pageIndex]);

  function addRemark(rect: Rect, text: string) {
    if (!selectedDocument || !page || !editing || revision.isPending) return;
    const remark: Remark = {
      ...rect,
      idx: nextNumber.current++,
      document_id: selectedDocument.id,
      page: page.page,
      text,
    };
    setRemarks((previous) => [...previous, remark]);
  }

  return (
    <div className={styles.kit}>
      <nav className={styles.documents} aria-label="Документы комплекта">
        <div className={styles.versions}>
          {[...detail.versions].reverse().map((item) => (
            <button
              type="button"
              key={item.version}
              className={item.version === version ? styles.versionSelected : styles.secondary}
              aria-pressed={item.version === version}
              disabled={revision.isPending}
              onClick={() => {
                selectVersion(item.version);
              }}
            >
              Версия {item.version}
              {item.version === detail.current_version ? " · сейчас" : ""}
            </button>
          ))}
        </div>
        {documents.map((document) => (
          <DocumentItem
            key={document.id}
            id={detail.id}
            version={version}
            document={document}
            selected={selectedDocument?.id === document.id}
            onSelect={() => {
              if (!revision.isPending) selectDocument(document.id);
            }}
          />
        ))}
      </nav>
      <section className={styles.viewer} aria-label="Просмотр комплекта">
        {!latest && (
          <p className={styles.versionNotice}>
            Это версия {version}. Последняя — версия {detail.current_version}.{" "}
            <a
              href={`?version=${detail.current_version}`}
              onClick={(event) => {
                event.preventDefault();
                selectVersion(detail.current_version);
              }}
            >
              Перейти к последней
            </a>
          </p>
        )}
        {selectedDocument && (
          <>
            <div className={styles.pageCaption}>
              <span>
                {selectedDocument.title}
                {page && ` · лист ${page.page} из ${selectedDocument.page_count}`}
              </span>
              {editing && <span>Нажмите на лист, чтобы оставить замечание в этом месте</span>}
            </div>
            {selectedDocument.page_count === 0 ? (
              <div className={styles.noPreview}>
                <p>Этот файл нельзя показать на сайте</p>
                <a href={selectedDocument.download_url}>Скачать</a>
              </div>
            ) : pagesQuery.isPending ? (
              <output>Загружаем…</output>
            ) : pagesQuery.isError ? (
              <button
                type="button"
                className={styles.secondary}
                onClick={() => {
                  void pagesQuery.refetch();
                }}
              >
                Повторить
              </button>
            ) : page ? (
              <>
                <PageCanvas
                  key={`${version}-${selectedDocument.id}-${page.page}`}
                  page={page}
                  title={selectedDocument.title}
                  enabled={editing && !revision.isPending}
                  number={nextNumber.current}
                  remarks={visibleRemarks.filter(
                    ({ remark }) =>
                      remark.document_id === selectedDocument.id && remark.page === page.page,
                  )}
                  onAdd={addRemark}
                />
                <div className={styles.pagination}>
                  <button
                    type="button"
                    className={styles.secondary}
                    aria-label="Предыдущая страница"
                    disabled={pageIndex <= 0}
                    onClick={() => {
                      const previous = pages[pageIndex - 1];
                      if (previous) setPageNumber(previous.page);
                    }}
                  >
                    ‹
                  </button>
                  <span>
                    стр. {page.page} из {selectedDocument.page_count}
                  </span>
                  <button
                    type="button"
                    className={styles.secondary}
                    aria-label="Следующая страница"
                    disabled={pageIndex >= pages.length - 1}
                    onClick={() => {
                      const next = pages[pageIndex + 1];
                      if (next) setPageNumber(next.page);
                    }}
                  >
                    ›
                  </button>
                </div>
                <div className={styles.thumbnails} aria-label="Страницы документа">
                  {pages.map((item) => (
                    <button
                      type="button"
                      className={
                        item.page === page.page ? styles.thumbSelected : styles.thumbButton
                      }
                      key={item.page}
                      aria-label={`Страница ${item.page}`}
                      aria-pressed={item.page === page.page}
                      onClick={() => {
                        setPageNumber(item.page);
                      }}
                    >
                      <img src={item.thumb_url} alt={`Страница ${item.page}`} loading="lazy" />
                      <span>{item.page}</span>
                    </button>
                  ))}
                </div>
              </>
            ) : (
              <div className={styles.noPreview}>
                <p>Этот файл нельзя показать на сайте</p>
                <a href={selectedDocument.download_url}>Скачать</a>
              </div>
            )}
          </>
        )}
      </section>
      <aside className={styles.revisionPanel} aria-label="Доработка">
        {canRevise && !editing && (
          <button
            type="button"
            className={styles.primary}
            onClick={() => {
              if (draftVersion !== version) {
                setDraftVersion(version);
                setRemarks([]);
                setComment("");
                setFiles([]);
                nextNumber.current = 1;
              }
              setMode(true);
            }}
          >
            Нужна доработка
          </button>
        )}
        <ol className={styles.remarkList}>
          {visibleRemarks.map(({ key, remark, draft }) => (
            <li key={key}>
              <button
                type="button"
                className={styles.remarkLink}
                onClick={() => {
                  selectDocument(remark.document_id, remark.page);
                }}
              >
                <span className={styles.remarkNumber}>{remark.idx}</span>
                <span>
                  <span className={styles.muted}>
                    стр. {remark.page},{" "}
                    {documents.find((item) => item.id === remark.document_id)?.title}
                  </span>
                  <span className={styles.remarkText}>{remark.text}</span>
                </span>
              </button>
              {draft && editing && (
                <button
                  type="button"
                  className={styles.textButton}
                  disabled={revision.isPending}
                  onClick={() => {
                    setRemarks((previous) => previous.filter((item) => item.idx !== remark.idx));
                  }}
                >
                  Удалить
                </button>
              )}
            </li>
          ))}
        </ol>
        {editing && (
          <form
            className={styles.revisionForm}
            onSubmit={(event) => {
              event.preventDefault();
              if ((comment.trim() || remarks.length) && !revision.isPending && !readingFiles)
                revision.mutate();
            }}
          >
            <label htmlFor="revision-comment">Общий комментарий</label>
            <textarea
              id="revision-comment"
              value={comment}
              disabled={revision.isPending}
              onChange={(event) => {
                setComment(event.target.value);
              }}
            />
            <FilePicker
              label="Приложить файлы"
              disabled={revision.isPending}
              files={files}
              onRemove={setFiles}
              onReadingChange={setReadingFiles}
              onAdd={(added) => {
                const merged = mergeFiles(files, added);
                if (exceedsLimit(merged)) {
                  show("Слишком много файлов: максимум 2 ГБ на заказ");
                  return;
                }
                setFiles(merged);
              }}
            />
            <button
              type="submit"
              className={styles.primary}
              disabled={
                revision.isPending || readingFiles || (!comment.trim() && remarks.length === 0)
              }
            >
              Отправить на доработку
            </button>
          </form>
        )}
      </aside>
    </div>
  );
}
