import { inputUploadUrl } from "@studlance/shared";
import { throwApiError } from "@studlance/shared";

// Upload queue for job input files (07-web-client.md):
// - PUT bytes per relative path, at most 3 files in parallel;
// - up to 3 retries per file (network errors and 5xx/429 only — a 4xx
//   rejection like 413 too_large is permanent);
// - the file body streams straight from the File object, never buffered;
// - overall progress is reported by completed bytes.

export interface UploadEntry {
  path: string;
  file: File;
}

export type EntryStatus = "pending" | "uploading" | "done" | "failed";

export interface UploadFailure {
  path: string;
  name: string;
  message: string;
  /** HTTP status of the failed request when the server answered (401, 413…). */
  status?: number;
}

export interface UploadProgress {
  /** status per path */
  statuses: Record<string, EntryStatus>;
  doneBytes: number;
  totalBytes: number;
  failures: UploadFailure[];
}

export interface UploadOutcome {
  succeeded: UploadEntry[];
  failed: UploadEntry[];
  failures: UploadFailure[];
}

export type FetchLike = (
  input: string,
  init: {
    method: string;
    body: BodyInit;
    credentials: RequestCredentials;
    headers: Record<string, string>;
  },
) => Promise<Response>;

const MAX_PARALLEL = 3;
const MAX_RETRIES = 3;
const RETRY_BASE_MS = 400;

function isPermanentFailure(status: number): boolean {
  return status >= 400 && status < 500 && status !== 429 && status !== 408;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export async function runUploads(
  jobId: string,
  entries: UploadEntry[],
  opts: {
    onProgress?: (p: UploadProgress) => void;
    signal?: AbortSignal;
    fetchImpl?: FetchLike;
    wait?: (ms: number) => Promise<void>;
  } = {},
): Promise<UploadOutcome> {
  const doFetch: FetchLike = opts.fetchImpl ?? ((input, init) => fetch(input, init));
  const wait = opts.wait ?? sleep;
  const statuses: Record<string, EntryStatus> = {};
  const failures: UploadProgress["failures"] = [];
  let doneBytes = 0;
  const totalBytes = entries.reduce((acc, e) => acc + (e.file.size || 0), 0);

  const report = () => {
    opts.onProgress?.({
      statuses: { ...statuses },
      doneBytes,
      totalBytes,
      failures: [...failures],
    });
  };
  for (const e of entries) {
    statuses[e.path] = "pending";
  }
  report();

  const succeeded: UploadEntry[] = [];
  const failed: UploadEntry[] = [];
  let cursor = 0;

  const uploadOne = async (entry: UploadEntry): Promise<void> => {
    statuses[entry.path] = "uploading";
    report();
    let lastMessage = "Не удалось загрузить файл";
    let lastStatus: number | undefined;
    for (let attempt = 0; attempt <= MAX_RETRIES; attempt++) {
      if (opts.signal?.aborted) {
        statuses[entry.path] = "pending";
        return;
      }
      try {
        const response = await doFetch(inputUploadUrl(jobId, entry.path), {
          method: "PUT",
          body: entry.file,
          credentials: "same-origin",
          headers: { "Content-Type": "application/octet-stream" },
        });
        await throwApiError(response);
        statuses[entry.path] = "done";
        doneBytes += entry.file.size || 0;
        succeeded.push(entry);
        report();
        return;
      } catch (err) {
        const status = (err as { status?: number }).status ?? 0;
        lastStatus = status || lastStatus;
        lastMessage = (err as Error).message || lastMessage;
        if (isPermanentFailure(status) || attempt === MAX_RETRIES || opts.signal?.aborted) {
          break;
        }
        await wait(RETRY_BASE_MS * (attempt + 1));
      }
    }
    statuses[entry.path] = "failed";
    const name = entry.path.split("/").pop() ?? entry.path;
    failures.push({
      path: entry.path,
      name,
      message: lastMessage,
      status: lastStatus,
    });
    failed.push(entry);
    report();
  };

  const workers = Array.from({ length: Math.min(MAX_PARALLEL, entries.length) }, async () => {
    while (cursor < entries.length && !opts.signal?.aborted) {
      const entry = entries[cursor];
      if (!entry) {
        break;
      }
      cursor += 1;
      await uploadOne(entry);
    }
  });
  await Promise.all(workers);

  return { succeeded, failed, failures };
}

/** "Загружаем… 45 %" — percent of completed bytes (07-web-client.md). */
export function progressPercent(p: UploadProgress): number {
  if (p.totalBytes === 0) {
    return 100;
  }
  return Math.floor((p.doneBytes / p.totalBytes) * 100);
}
