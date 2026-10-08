import {
  type Document,
  type Page,
  type Remark,
  api,
  runInSession,
  useCurrentUser,
} from "@studlance/shared";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { requireData } from "../api";

// Center column (08-web-admin.md): draft and finished versions, document list,
// page viewer with real changed_boxes (orange) and the client's remark boxes.
// The version a remark belongs to is revision.version - 1, never the remark's
// document id alone (ids are reissued per snapshot).

export interface AdminDetail {
  id: string;
  current_version: number;
  draft?: { documents: Document[] } | null;
  versions: { version: number; documents: Document[] }[];
  revisions: {
    version: number;
    comment: string;
    remarks: Remark[];
  }[];
}

function remarkVersion(revisionVersion: number): number {
  return revisionVersion - 1;
}

export interface SheetJump {
  jobId: string;
  version: number;
  documentId: string;
  page: number;
  nonce: number;
}

export function SheetsPanel({ detail, jump }: { detail: AdminDetail; jump?: SheetJump | null }) {
  const { data: user } = useCurrentUser();
  const [showChanges, setShowChanges] = useState(true);
  const [source, setSource] = useState<string>(() => {
    if (detail.draft?.documents?.length) return "draft";
    return detail.versions.length > 0
      ? `v${detail.versions[detail.versions.length - 1]?.version}`
      : "draft";
  });
  const [documentId, setDocumentId] = useState("");
  const [pageNumber, setPageNumber] = useState(1);

  const documents = useMemo<Document[]>(() => {
    if (source === "draft") {
      return detail.draft?.documents ?? [];
    }
    const version = Number(source.replace("v", ""));
    return detail.versions.find((item) => item.version === version)?.documents ?? [];
  }, [detail, source]);

  const selectedDocument = documents.find((doc) => doc.id === documentId) ?? documents[0];

  const versionNumber = source === "draft" ? 0 : Number(source.replace("v", ""));

  const pagesQuery = useQuery({
    queryKey: ["admin-pages", user?.id, detail.id, source, selectedDocument?.id],
    enabled: !!selectedDocument && selectedDocument.page_count > 0,
    staleTime: Number.POSITIVE_INFINITY,
    queryFn: async () =>
      runInSession(async () => {
        const doc = selectedDocument as Document;
        const data = requireData(
          source === "draft"
            ? await api.GET("/api/admin/jobs/{id}/draft/documents/{document_id}/pages", {
                params: { path: { id: detail.id, document_id: doc.id } },
              })
            : await api.GET("/api/admin/jobs/{id}/versions/{v}/documents/{document_id}/pages", {
                params: {
                  path: { id: detail.id, v: Number(source.replace("v", "")), document_id: doc.id },
                },
              }),
        );
        return [...data.pages].sort((a, b) => a.page - b.page);
      }),
  });

  const pages = pagesQuery.data ?? [];
  const page = pages.find((item) => item.page === pageNumber) ?? pages[0];

  // Remarks of the client that belong to the shown version.
  const remarks = useMemo(() => {
    if (source === "draft") {
      return [] as { key: string; remark: Remark }[];
    }
    const wanted = versionNumber;
    return detail.revisions
      .filter((revision) => remarkVersion(revision.version) === wanted)
      .flatMap((revision) =>
        revision.remarks.map((remark) => ({ key: `${revision.version}-${remark.idx}`, remark })),
      );
  }, [detail, source, versionNumber]);

  // Reset the document/page when the version switches. `source` is the trigger.
  // biome-ignore lint/correctness/useExhaustiveDependencies: source is the intended reset trigger.
  useEffect(() => {
    setDocumentId("");
    setPageNumber(1);
  }, [source]);

  // A remark link switches to its version, document and page.
  useEffect(() => {
    if (!jump) {
      return;
    }
    setSource(jump.version > 0 ? `v${jump.version}` : "draft");
    setDocumentId(jump.documentId);
    setPageNumber(jump.page);
  }, [jump]);

  const sources = [
    ...(detail.draft?.documents?.length ? [{ value: "draft", label: "Черновик" }] : []),
    ...[...detail.versions]
      .reverse()
      .map((version) => ({ value: `v${version.version}`, label: `Версия ${version.version}` })),
  ];

  return (
    <section className="sl-admin-sheets" aria-label="Листы комплекта">
      <div className="sl-admin-sheets-head">
        <div className="sl-admin-version-switch" role="tablist" aria-label="Черновик и версии">
          {sources.map((item) => (
            <button
              type="button"
              role="tab"
              key={item.value}
              aria-selected={source === item.value}
              className={source === item.value ? "sl-admin-version--active" : ""}
              onClick={() => setSource(item.value)}
            >
              {item.label}
            </button>
          ))}
        </div>
        <label className="sl-admin-changes-toggle">
          <input
            type="checkbox"
            checked={showChanges}
            onChange={(event) => setShowChanges(event.target.checked)}
          />
          <span>Показать правки</span>
        </label>
      </div>

      <div className="sl-admin-doc-list" role="tablist" aria-label="Документы">
        {documents.map((doc) => (
          <button
            type="button"
            role="tab"
            key={doc.id}
            aria-selected={selectedDocument?.id === doc.id}
            className={selectedDocument?.id === doc.id ? "sl-admin-doc--active" : ""}
            onClick={() => {
              setDocumentId(doc.id);
              setPageNumber(1);
            }}
          >
            {doc.title}
            <span className="sl-admin-muted"> · {doc.kind}</span>
          </button>
        ))}
      </div>

      {!selectedDocument ? (
        <p className="sl-admin-state">Нет документов</p>
      ) : (
        <div className="sl-admin-viewer">
          <div className="sl-admin-viewer-head">
            <span>
              {selectedDocument.title}
              {page ? ` · стр. ${page.page} из ${selectedDocument.page_count}` : ""}
            </span>
            <a href={selectedDocument.download_url}>Скачать файл</a>
          </div>
          {selectedDocument.page_count === 0 ? (
            <p className="sl-admin-state">Этот файл нельзя показать на сайте</p>
          ) : pagesQuery.isPending ? (
            <output className="sl-admin-state">Загружаем…</output>
          ) : pagesQuery.isError ? (
            <button
              type="button"
              className="sl-admin-button"
              onClick={() => {
                void pagesQuery.refetch();
              }}
            >
              Повторить
            </button>
          ) : page ? (
            <>
              <PageSheetImpl
                page={page}
                showChanges={showChanges}
                remarks={remarks
                  .filter(
                    ({ remark }) =>
                      remark.document_id === selectedDocument.id && remark.page === page.page,
                  )
                  .map(({ remark }) => remark)}
              />
              <div className="sl-admin-pagination">
                <button
                  type="button"
                  className="sl-admin-button sl-admin-button--ghost"
                  disabled={!page || pages.indexOf(page) === 0}
                  onClick={() => {
                    const index = page ? pages.indexOf(page) : 0;
                    const previous = pages[index - 1];
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
                  className="sl-admin-button sl-admin-button--ghost"
                  disabled={!page || pages.indexOf(page) === pages.length - 1}
                  onClick={() => {
                    const index = page ? pages.indexOf(page) : 0;
                    const next = pages[index + 1];
                    if (next) setPageNumber(next.page);
                  }}
                >
                  ›
                </button>
              </div>
              <div className="sl-admin-thumbs" aria-label="Страницы документа">
                {pages.map((item) => (
                  <button
                    type="button"
                    key={item.page}
                    aria-label={`Страница ${item.page}`}
                    aria-pressed={item.page === page.page}
                    className={item.page === page.page ? "sl-admin-thumb--active" : ""}
                    onClick={() => setPageNumber(item.page)}
                  >
                    <img src={item.thumb_url} alt={`Страница ${item.page}`} loading="lazy" />
                  </button>
                ))}
              </div>
            </>
          ) : (
            <p className="sl-admin-state">Этот файл нельзя показать на сайте</p>
          )}
        </div>
      )}
    </section>
  );
}

// The sheet with overlays. Kept separate so the box math is obvious.
function PageSheetImpl({
  page,
  showChanges,
  remarks,
}: {
  page: Page;
  showChanges: boolean;
  remarks: Remark[];
}) {
  return (
    <div className="sl-admin-sheet">
      <img src={page.image_url} alt={`Лист ${page.page}`} width={page.width} height={page.height} />
      {showChanges &&
        (page.changed_boxes ?? []).map((box) => (
          <span
            key={`changed-${box.x}-${box.y}-${box.w}-${box.h}`}
            className="sl-admin-box sl-admin-box--changed"
            style={{
              left: `${box.x * 100}%`,
              top: `${box.y * 100}%`,
              width: `${box.w * 100}%`,
              height: `${box.h * 100}%`,
            }}
          />
        ))}
      {remarks.map((remark) => (
        <span
          key={`remark-${remark.idx}`}
          className="sl-admin-box sl-admin-box--remark"
          title={remark.text}
          style={{
            left: `${remark.x * 100}%`,
            top: `${remark.y * 100}%`,
            width: `${remark.w * 100}%`,
            height: `${remark.h * 100}%`,
          }}
        >
          <span className="sl-admin-box-number">{remark.idx}</span>
        </span>
      ))}
    </div>
  );
}
