import {
  ApiError,
  api,
  apiErrorMessage,
  expireSession,
  isCurrentSession,
  jobsListQueryKey,
  plural,
  sessionGeneration,
  tagSessionError,
  useCurrentUser,
  useToast,
} from "@studlance/shared";
import { useQueryClient } from "@tanstack/react-query";
import { type ChangeEvent, type DragEvent, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  type PickedFile,
  chipsFromFiles,
  exceedsLimit,
  filesFromDataTransfer,
  filesFromInput,
  mergeFiles,
  removeChip,
} from "../../files";
import { progressPercent, runUploads } from "../../uploadQueue";
import styles from "./Home.module.css";

export function OrderForm() {
  const [prompt, setPrompt] = useState("");
  const [files, setFiles] = useState<PickedFile[]>([]);
  const [busy, setBusy] = useState(false);
  const [collecting, setCollecting] = useState(false);
  const [retry, setRetry] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [percent, setPercent] = useState(0);
  const [dragging, setDragging] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const selected = useRef<PickedFile[]>([]);
  const jobId = useRef<string | null>(null);
  const pending = useRef<PickedFile[]>([]);
  const completedBytes = useRef(0);
  const totalBytes = useRef(0);
  const locked = useRef(false);
  const reads = useRef(0);
  const dragDepth = useRef(0);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const filesInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const menuButton = useRef<HTMLButtonElement>(null);
  const { show } = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: sessionUser } = useCurrentUser();
  const frozen = busy || jobId.current !== null;

  // 16px text, 24px line height, 4px vertical padding: two to ten rows.
  // The DOM already holds the typed value when onChange fires, so measuring
  // there keeps the effect free of the prompt dependency.
  const autosize = () => {
    const element = textarea.current;
    if (!element) return;
    element.style.height = "auto";
    element.style.height = `${Math.min(244, Math.max(52, element.scrollHeight))}px`;
  };

  useEffect(autosize, []);

  useEffect(() => {
    if (!menuOpen) return;
    menu.current?.querySelector<HTMLButtonElement>("button")?.focus();
    const close = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        !menu.current?.contains(event.target) &&
        !menuButton.current?.contains(event.target)
      )
        setMenuOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [menuOpen]);

  function addFiles(added: PickedFile[]) {
    if (locked.current || jobId.current) return;
    const merged = mergeFiles(selected.current, added);
    if (exceedsLimit(merged)) {
      show("Слишком много файлов: максимум 2 ГБ на заказ");
      return;
    }
    selected.current = merged;
    setFiles(merged);
  }

  function pick(event: ChangeEvent<HTMLInputElement>) {
    addFiles(filesFromInput(event.currentTarget.files));
    event.currentTarget.value = "";
  }

  async function drop(event: DragEvent<HTMLFormElement>) {
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    if (locked.current || jobId.current) return;
    reads.current += 1;
    setCollecting(true);
    try {
      // Capture entries synchronously while the drop's DataTransfer is readable.
      addFiles(await filesFromDataTransfer(event.dataTransfer));
    } catch (error) {
      show(apiErrorMessage(error));
    } finally {
      reads.current -= 1;
      setCollecting(reads.current > 0);
    }
  }

  async function submit() {
    if (locked.current || reads.current > 0) return;
    if (!jobId.current && prompt.trim().length < 20 && selected.current.length === 0) return;
    locked.current = true;
    setBusy(true);
    setMenuOpen(false);
    // The whole create→upload→submit flow belongs to the session instance it
    // started in: after a session switch (expiry, logout, another login) its
    // late results and errors are ignored before any side effect — no cache
    // writes, no navigation, no session expiry, no toasts, no form resets.
    const generation = sessionGeneration();
    const isMine = () => isCurrentSession(generation);
    const guardError = (error: unknown) => tagSessionError(error, generation);
    try {
      if (!jobId.current) {
        const { data, error, response } = await api.POST("/api/client/jobs", {
          body: { prompt: prompt.trim() },
        });
        if (error)
          throw guardError(new ApiError(response.status, error.error.code, error.error.message));
        if (!isMine()) return;
        jobId.current = data.id;
        pending.current = [...selected.current];
        totalBytes.current = selected.current.reduce((sum, entry) => sum + entry.file.size, 0);
      }
      const id = jobId.current;
      if (pending.current.length > 0) {
        setUploading(true);
        const outcome = await runUploads(id, pending.current, {
          onProgress: (progress) => {
            if (!isMine()) return;
            setPercent(
              progressPercent({
                ...progress,
                doneBytes: completedBytes.current + progress.doneBytes,
                totalBytes: totalBytes.current,
              }),
            );
          },
          fetchImpl: async (input, init) => {
            const response = await fetch(input, init);
            if (response.status === 401 && isMine())
              navigate(`/login?next=${encodeURIComponent(`/orders/${id}`)}`, { replace: true });
            return response;
          },
        });
        if (!isMine()) return;
        completedBytes.current += outcome.succeeded.reduce(
          (sum, entry) => sum + entry.file.size,
          0,
        );
        pending.current = outcome.failed;
        if (outcome.failed.length > 0) {
          for (const failure of outcome.failures) {
            // Preserve server errors, including 413, alongside the retry instruction.
            if (failure.message) show(failure.message);
            show(`Не удалось загрузить ${failure.name}. Попробуйте ещё раз`);
          }
          setRetry(true);
          return;
        }
      }
      if (!isMine()) return;
      setUploading(false);
      const { error, response } = await api.POST("/api/client/jobs/{id}/submit", {
        params: { path: { id } },
      });
      if (error)
        throw guardError(new ApiError(response.status, error.error.code, error.error.message));
      if (!isMine()) return;
      await queryClient.invalidateQueries({ queryKey: jobsListQueryKey(sessionUser?.id) });
      navigate(`/orders/${id}`);
    } catch (error) {
      if (!isMine()) {
        // A late error of an old session instance: ignore entirely.
        return;
      }
      if (error instanceof ApiError && error.status === 401) {
        // End the session locally and let the auth gate open the login form;
        // a stale cached me must not bounce the user off it.
        expireSession(queryClient);
        const next = jobId.current ? `/orders/${jobId.current}` : "/";
        navigate(`/login?next=${encodeURIComponent(next)}`, { replace: true });
      } else {
        show(apiErrorMessage(error));
      }
      setRetry(true);
    } finally {
      if (isMine()) {
        locked.current = false;
        setBusy(false);
        setUploading(false);
      }
    }
  }

  return (
    <section className={styles.orderSection}>
      <div className={styles.orderContainer}>
        <div className={styles.orderIntro}>
          <h1>Что нужно сделать?</h1>
          <p>Опишите задачу и приложите всё, что есть: задание, методичку, вариант, примеры.</p>
        </div>
        <form
          className={`${styles.orderForm} ${dragging ? styles.dragging : ""}`}
          aria-busy={busy || collecting}
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
          onDragEnter={(event) => {
            if (!event.dataTransfer.types.includes("Files")) return;
            event.preventDefault();
            dragDepth.current += 1;
            if (!frozen) setDragging(true);
          }}
          onDragOver={(event) => {
            if (!event.dataTransfer.types.includes("Files")) return;
            event.preventDefault();
            event.dataTransfer.dropEffect = frozen ? "none" : "copy";
          }}
          onDragLeave={(event) => {
            event.preventDefault();
            dragDepth.current = Math.max(0, dragDepth.current - 1);
            if (dragDepth.current === 0) setDragging(false);
          }}
          onDrop={(event) => void drop(event)}
        >
          <label htmlFor="request" className={styles.visuallyHidden}>
            Запрос
          </label>
          <textarea
            ref={textarea}
            id="request"
            rows={2}
            className={styles.request}
            value={prompt}
            onChange={(event) => {
              setPrompt(event.target.value);
              autosize();
            }}
            disabled={frozen}
            placeholder="Например: курсовая работа, вариант 14, всё по методичке кафедры"
          />
          {files.length > 0 && (
            <div className={styles.chips}>
              {chipsFromFiles(files).map((chip) => (
                <span className={styles.chip} key={chip.key}>
                  <span>
                    {chip.name}
                    {chip.kind === "folder" &&
                      ` · ${chip.count} ${plural(chip.count, "файл", "файла", "файлов")}`}
                  </span>
                  <button
                    type="button"
                    aria-label="Убрать"
                    disabled={frozen}
                    onClick={() => {
                      selected.current = removeChip(selected.current, chip);
                      setFiles(selected.current);
                    }}
                  >
                    ×
                  </button>
                </span>
              ))}
            </div>
          )}
          <div className={styles.formActions}>
            <div className={styles.attachContainer}>
              <button
                ref={menuButton}
                className={styles.attachButton}
                type="button"
                disabled={frozen}
                aria-expanded={menuOpen}
                aria-controls="attachment-options"
                aria-haspopup="menu"
                onClick={() => setMenuOpen((open) => !open)}
              >
                <svg
                  role="img"
                  aria-label="Прикрепить"
                  width="18"
                  height="18"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.8"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                >
                  <title>Прикрепить</title>
                  <path d="m21 11-8.6 8.6a5 5 0 0 1-7.1-7.1l8.6-8.6a3.3 3.3 0 0 1 4.7 4.7l-8.6 8.6a1.7 1.7 0 0 1-2.4-2.4l7.9-7.9" />
                </svg>
                Прикрепить файлы или папку
              </button>
              {menuOpen && (
                <div
                  ref={menu}
                  id="attachment-options"
                  role="menu"
                  aria-label="Прикрепить"
                  className={styles.attachMenu}
                  onBlur={(event) => {
                    if (!event.currentTarget.contains(event.relatedTarget)) setMenuOpen(false);
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Escape") {
                      setMenuOpen(false);
                      menuButton.current?.focus();
                    }
                    if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
                      event.preventDefault();
                      const options = Array.from(
                        event.currentTarget.querySelectorAll<HTMLButtonElement>("button"),
                      );
                      const index = options.indexOf(document.activeElement as HTMLButtonElement);
                      const next =
                        event.key === "Home"
                          ? 0
                          : event.key === "End"
                            ? options.length - 1
                            : (index + (event.key === "ArrowUp" ? -1 : 1) + options.length) %
                              options.length;
                      options[next]?.focus();
                    }
                  }}
                >
                  <button
                    type="button"
                    role="menuitem"
                    onClick={() => {
                      filesInput.current?.click();
                      setMenuOpen(false);
                      menuButton.current?.focus();
                    }}
                  >
                    Файлы
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    onClick={() => {
                      folderInput.current?.click();
                      setMenuOpen(false);
                      menuButton.current?.focus();
                    }}
                  >
                    Папку
                  </button>
                </div>
              )}
              <label className={styles.visuallyHidden} htmlFor="order-files">
                Файлы
              </label>
              <input
                hidden
                id="order-files"
                ref={filesInput}
                type="file"
                multiple
                onChange={pick}
                disabled={frozen}
              />
              <label className={styles.visuallyHidden} htmlFor="order-folder">
                Папку
              </label>
              <input
                hidden
                id="order-folder"
                ref={folderInput}
                type="file"
                multiple
                {...{ webkitdirectory: "" }}
                onChange={pick}
                disabled={frozen}
              />
            </div>
            <button
              className={styles.submitButton}
              type="submit"
              disabled={
                busy || collecting || (!retry && prompt.trim().length < 20 && files.length === 0)
              }
              aria-live="polite"
            >
              {uploading ? `Загружаем… ${percent} %` : retry ? "Повторить" : "Оформить заказ"}
            </button>
          </div>
        </form>
      </div>
    </section>
  );
}
