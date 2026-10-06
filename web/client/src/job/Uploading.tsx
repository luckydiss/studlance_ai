import { type JobDetail, api, formatBytes, setJobCache, useToast } from "@studlance/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { MAX_TOTAL_UPLOAD_BYTES, type PickedFile, exceedsLimit, mergeFiles } from "../files";
import { type UploadEntry, type UploadProgress, progressPercent, runUploads } from "../uploadQueue";
import { FilePicker } from "./FilePicker";
import styles from "./Job.module.css";
import { requireData, useInvalidateJob, useJobError } from "./api";

export function Uploading({ detail }: { detail: JobDetail }) {
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const [progress, setProgress] = useState<UploadProgress>();
  const [failed, setFailed] = useState<UploadEntry[]>([]);
  const uploaded = useRef<UploadEntry[]>([]);
  const controller = useRef<AbortController>();
  const client = useQueryClient();
  const invalidate = useInvalidateJob(detail.id);
  const onError = useJobError();
  const { show } = useToast();
  useEffect(() => () => controller.current?.abort(), []);
  const submit = useMutation({
    mutationFn: async () =>
      requireData(
        await api.POST("/api/client/jobs/{id}/submit", { params: { path: { id: detail.id } } }),
      ),
    onSuccess: (data) => {
      setJobCache(client, detail.id, data);
      void invalidate();
    },
    onError,
  });

  async function upload(entries: UploadEntry[]) {
    if (lock.current || entries.length === 0) return;
    lock.current = true;
    setBusy(true);
    controller.current = new AbortController();
    try {
      const result = await runUploads(detail.id, entries, {
        signal: controller.current.signal,
        onProgress: setProgress,
      });
      if (controller.current.signal.aborted) return;
      uploaded.current = mergeFiles(uploaded.current, result.succeeded);
      setFailed((previous) =>
        mergeFiles(
          previous.filter((entry) => !entries.some((item) => item.path === entry.path)),
          result.failed,
        ),
      );
      for (const failure of result.failures) {
        show(`Не удалось загрузить ${failure.name}. Попробуйте ещё раз`);
        if (failure.message) onError(new Error(failure.message));
      }
      await invalidate();
    } catch (error) {
      onError(error);
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }

  function add(files: PickedFile[]) {
    const local = mergeFiles(mergeFiles(uploaded.current, failed), files);
    const sizes = new Map(detail.input_files.map((file) => [file.path, file.size]));
    for (const file of local) sizes.set(file.path, file.file.size);
    const total = Array.from(sizes.values()).reduce((sum, size) => sum + size, 0);
    if (exceedsLimit(local) || total > MAX_TOTAL_UPLOAD_BYTES) {
      show("Слишком много файлов: максимум 2 ГБ на заказ");
      return;
    }
    void upload(files);
  }

  const savedFiles = new Map(detail.input_files.map((file) => [file.path, file.size]));
  for (const file of uploaded.current) savedFiles.set(file.path, file.file.size);
  const percent = busy && progress ? progressPercent(progress) : undefined;
  return (
    <div className={styles.uploadCard}>
      <ul className={styles.inputFiles}>
        {Array.from(savedFiles).map(([path, size]) => (
          <li key={path}>
            <span>{path}</span>
            <span className={styles.muted}>{formatBytes(size)}</span>
          </li>
        ))}
      </ul>
      <FilePicker onAdd={add} disabled={busy || submit.isPending} />
      {percent !== undefined && <output>Загружаем… {percent} %</output>}
      {progress && (
        <progress
          className={styles.progress}
          aria-label="Загрузка файлов"
          value={progressPercent(progress)}
          max={100}
        />
      )}
      {failed.length > 0 && (
        <ul className={styles.inputFiles}>
          {failed.map((file) => (
            <li key={file.path}>
              <span>Не удалось загрузить {file.path}</span>
              <button
                className={styles.secondary}
                type="button"
                disabled={busy || submit.isPending}
                onClick={() => {
                  void upload([file]);
                }}
              >
                Повторить
              </button>
            </li>
          ))}
        </ul>
      )}
      <button
        className={styles.primary}
        type="button"
        disabled={
          busy || submit.isPending || (savedFiles.size === 0 && detail.prompt.trim().length < 20)
        }
        onClick={() => submit.mutate()}
      >
        {percent !== undefined ? `Загружаем… ${percent} %` : "Отправить заказ"}
      </button>
    </div>
  );
}
