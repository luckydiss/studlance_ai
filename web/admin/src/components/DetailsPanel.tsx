import {
  type Remark,
  adminInputUrl,
  api,
  apiErrorStatus,
  formatDate,
  isCurrentSession,
  runInSession,
  sessionGeneration,
} from "@studlance/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { requireData, useAdminError } from "../api";
import { StaleAdminViewError, adminJobKey } from "../session";

// Right column (08-web-admin.md): verification, client revisions and their
// attachments, source files, the event feed and admin notes.

export interface AdminInputFile {
  path: string;
  size: number;
  revision: number;
}

export interface RevisionView {
  version: number;
  comment: string;
  created_at: string;
  completed_at?: string | null;
  remarks: Remark[];
}

export interface DetailsDetail {
  id: string;
  input_files: AdminInputFile[];
  revisions: RevisionView[];
  verification?: {
    found: number;
    fixed: number;
    remaining: { severity: string; description: string }[];
  } | null;
  events: { ts: string; kind: string; data: Record<string, unknown> }[];
  notes: { id: string; author: string; text: string; created_at: string }[];
}

export function DetailsPanel({
  detail,
  userId,
  viewToken,
  isViewCurrent,
  onOpenRemark,
}: {
  detail: DetailsDetail;
  userId: string | null | undefined;
  viewToken: symbol;
  isViewCurrent: (token: symbol) => boolean;
  onOpenRemark: (version: number, documentId: string, page: number) => void;
}) {
  const sources = detail.input_files.filter((file) => file.revision === 0);
  const attachmentsByRevision = new Map<number, AdminInputFile[]>();
  for (const file of detail.input_files) {
    if (file.revision <= 0) {
      continue;
    }
    const list = attachmentsByRevision.get(file.revision) ?? [];
    list.push(file);
    attachmentsByRevision.set(file.revision, list);
  }

  return (
    <aside className="sl-admin-details" aria-label="Проверка, доработки и файлы">
      <VerificationSection verification={detail.verification} />

      <section className="sl-admin-detail-block">
        <h2>Доработки клиента</h2>
        {detail.revisions.length === 0 ? (
          <p className="sl-admin-muted">Доработок не было</p>
        ) : (
          <ol className="sl-admin-revisions">
            {detail.revisions.map((revision) => {
              const attachmentRevision = revision.version;
              const files = attachmentsByRevision.get(attachmentRevision) ?? [];
              return (
                <li key={revision.version} className="sl-admin-revision">
                  <div className="sl-admin-revision-head">
                    Доработка {revision.version}
                    <span className="sl-admin-muted">
                      {" "}
                      · {formatDate(revision.created_at)}
                      {revision.completed_at ? " · завершена" : " · в работе"}
                    </span>
                  </div>
                  {revision.comment && (
                    <p className="sl-admin-revision-comment">{revision.comment}</p>
                  )}
                  {revision.remarks.length > 0 && (
                    <ul className="sl-admin-remark-list">
                      {revision.remarks.map((remark) => (
                        <li key={remark.idx}>
                          <button
                            type="button"
                            className="sl-admin-remark-link"
                            onClick={() =>
                              // The remark was made on the previous version.
                              onOpenRemark(revision.version - 1, remark.document_id, remark.page)
                            }
                          >
                            <span className="sl-admin-muted">
                              стр. {remark.page}, замечание {remark.idx}
                            </span>
                            <span>{remark.text}</span>
                          </button>
                          {remark.document_id && (
                            <a
                              className="sl-admin-muted"
                              href={`#remark-${revision.version}-${remark.idx}`}
                              onClick={(event) => {
                                event.preventDefault();
                                onOpenRemark(revision.version - 1, remark.document_id, remark.page);
                              }}
                            >
                              открыть лист
                            </a>
                          )}
                        </li>
                      ))}
                    </ul>
                  )}
                  {files.length > 0 && (
                    <ul className="sl-admin-file-list">
                      {files.map((file) => (
                        <li key={file.path}>
                          <a href={adminInputUrl(detail.id, file.path)}>{file.path}</a>
                        </li>
                      ))}
                    </ul>
                  )}
                </li>
              );
            })}
          </ol>
        )}
      </section>

      <section className="sl-admin-detail-block">
        <h2>Исходные файлы</h2>
        {sources.length === 0 ? (
          <p className="sl-admin-muted">Исходников нет</p>
        ) : (
          <ul className="sl-admin-file-list">
            {sources.map((file) => (
              <li key={file.path}>
                <a href={adminInputUrl(detail.id, file.path)}>{file.path}</a>
                <span className="sl-admin-muted"> · {file.size} Б</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="sl-admin-detail-block">
        <h2>События</h2>
        {detail.events.length === 0 ? (
          <p className="sl-admin-muted">Событий нет</p>
        ) : (
          <ol className="sl-admin-event-list">
            {detail.events.map((event, index) => (
              <li key={`${event.ts}-${index}`}>
                <span className="sl-admin-muted">{formatDate(event.ts)}</span> {event.kind}
              </li>
            ))}
          </ol>
        )}
      </section>

      <NotesSection
        detail={detail}
        userId={userId}
        viewToken={viewToken}
        isViewCurrent={isViewCurrent}
      />
    </aside>
  );
}

function VerificationSection({ verification }: { verification: DetailsDetail["verification"] }) {
  if (!verification) {
    return (
      <section className="sl-admin-detail-block">
        <h2>Проверка</h2>
        <p className="sl-admin-muted">Проверки ещё не было</p>
      </section>
    );
  }
  return (
    <section className="sl-admin-detail-block">
      <h2>
        Проверка{" "}
        <span className="sl-admin-muted">
          · найдено {verification.found}, исправлено {verification.fixed}
        </span>
      </h2>
      {verification.remaining.length === 0 ? (
        <p className="sl-admin-muted">Замечаний не осталось</p>
      ) : (
        <ul className="sl-admin-verification">
          {verification.remaining.map((item, index) => (
            <li key={`${item.severity}-${index}`}>
              <span className={`sl-admin-severity sl-admin-severity--${item.severity}`}>
                {item.severity}
              </span>
              {item.description}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function NotesSection({
  detail,
  userId,
  viewToken,
  isViewCurrent,
}: {
  detail: DetailsDetail;
  userId: string | null | undefined;
  viewToken: symbol;
  isViewCurrent: (token: symbol) => boolean;
}) {
  const [text, setText] = useState("");
  const onError = useAdminError();
  const queryClient = useQueryClient();
  const generation = sessionGeneration();
  const note = useMutation({
    mutationFn: async (variables: {
      jobId: string;
      text: string;
      generation: number;
      viewToken: symbol;
    }) => {
      try {
        return await runInSession(
          async () =>
            requireData(
              await api.POST("/api/admin/jobs/{id}/notes", {
                params: { path: { id: variables.jobId } },
                body: { text: variables.text },
              }),
            ),
          variables.generation,
        );
      } catch (error) {
        if (variables.jobId !== detail.id || !isViewCurrent(variables.viewToken)) {
          throw new StaleAdminViewError(error);
        }
        throw error;
      }
    },
    onSuccess: async (_data, variables) => {
      if (
        !isCurrentSession(variables.generation) ||
        variables.jobId !== detail.id ||
        !isViewCurrent(variables.viewToken)
      )
        return;
      setText("");
      await queryClient.invalidateQueries({ queryKey: adminJobKey(userId, variables.jobId) });
    },
    onError: (error, variables) => {
      if (
        !isCurrentSession(variables.generation) ||
        variables.jobId !== detail.id ||
        !isViewCurrent(variables.viewToken)
      )
        return;
      if (apiErrorStatus(error) === 409) {
        void queryClient.invalidateQueries({ queryKey: adminJobKey(userId, detail.id) });
      }
      onError(error);
    },
  });
  // biome-ignore lint/correctness/useExhaustiveDependencies: route job id is the reset trigger.
  useEffect(() => {
    setText("");
    note.reset();
  }, [detail.id, note.reset]);

  return (
    <section className="sl-admin-detail-block">
      <h2>Заметки</h2>
      <form
        className="sl-admin-note-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (text.trim() && !note.isPending) {
            note.mutate({ jobId: detail.id, text: text.trim(), generation, viewToken });
          }
        }}
      >
        <label htmlFor="admin-note">Заметка</label>
        <textarea
          id="admin-note"
          value={text}
          disabled={note.isPending}
          onChange={(event) => {
            setText(event.target.value);
          }}
        />
        <button type="submit" className="sl-admin-button" disabled={!text.trim() || note.isPending}>
          Добавить
        </button>
      </form>
      <ol className="sl-admin-note-list">
        {detail.notes.map((item) => (
          <li key={item.id}>
            <span className="sl-admin-muted">{formatDate(item.created_at)}</span>
            <p>{item.text}</p>
          </li>
        ))}
      </ol>
    </section>
  );
}
