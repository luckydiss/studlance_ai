// Selecting files and folders with relative paths (07-web-client.md):
// - input multiple → file names;
// - input webkitdirectory → webkitRelativePath (includes the root folder);
// - drag&drop → webkitGetAsEntry recursion, keeping relative paths.

export interface PickedFile {
  path: string;
  file: File;
}

const MAX_TOTAL_BYTES = 2 * 1024 * 1024 * 1024; // --max-upload, 2 GiB

/**
 * Normalizes "/" separators and drops empty and traversal segments ("..",
 * "."). Segment names are preserved byte-for-byte: spaces in file and folder
 * names are valid and must survive (07-web-client.md).
 */
export function normalizeRelPath(path: string): string {
  return path
    .split(/[\\/]+/)
    .filter((p) => p.length > 0 && p !== "." && p !== "..")
    .join("/");
}

export function filesFromInput(files: FileList | null): PickedFile[] {
  if (!files) {
    return [];
  }
  const out: PickedFile[] = [];
  for (const file of Array.from(files)) {
    const rel = normalizeRelPath(file.webkitRelativePath || file.name);
    if (rel) {
      out.push({ path: rel, file });
    }
  }
  return out;
}

interface FsEntry {
  isFile: boolean;
  isDirectory: boolean;
  name: string;
  file: (cb: (f: File) => void, err: (e: unknown) => void) => void;
  createReader: () => {
    readEntries: (cb: (entries: FsEntry[]) => void, err: (e: unknown) => void) => void;
  };
}

function readAllEntries(dir: FsEntry): Promise<FsEntry[]> {
  const reader = dir.createReader();
  const all: FsEntry[] = [];
  return new Promise((resolve, reject) => {
    const step = () => {
      reader.readEntries((batch) => {
        if (batch.length === 0) {
          resolve(all);
          return;
        }
        all.push(...batch);
        step();
      }, reject);
    };
    step();
  });
}

function entryFile(entry: FsEntry): Promise<File> {
  return new Promise((resolve, reject) => entry.file(resolve, reject));
}

async function walkEntry(entry: FsEntry, prefix: string, out: PickedFile[]): Promise<void> {
  if (entry.isFile) {
    const file = await entryFile(entry);
    const path = normalizeRelPath(prefix ? `${prefix}/${entry.name}` : entry.name);
    if (path) {
      out.push({ path, file });
    }
    return;
  }
  if (!entry.isDirectory) {
    return;
  }
  const children = await readAllEntries(entry);
  const nextPrefix = prefix ? `${prefix}/${entry.name}` : entry.name;
  for (const child of children) {
    await walkEntry(child, nextPrefix, out);
  }
}

/** Collects dropped files; folders are walked with relative paths kept. */
export async function filesFromDataTransfer(dt: DataTransfer): Promise<PickedFile[]> {
  const out: PickedFile[] = [];
  const entries: FsEntry[] = [];
  if (dt.items && dt.items.length > 0) {
    for (const item of Array.from(dt.items)) {
      if (item.kind !== "file") {
        continue;
      }
      const getter = (item as DataTransferItem & { webkitGetAsEntry?: () => FsEntry | null })
        .webkitGetAsEntry;
      const entry = getter ? getter.call(item) : null;
      if (entry) {
        entries.push(entry);
      }
    }
  }
  if (entries.length > 0) {
    for (const entry of entries) {
      await walkEntry(entry, "", out);
    }
    return out;
  }
  // No entries API: plain files without folders.
  return filesFromInput(dt.files);
}

export function totalBytes(files: PickedFile[]): number {
  return files.reduce((acc, f) => acc + f.file.size, 0);
}

export function exceedsLimit(files: PickedFile[]): boolean {
  return totalBytes(files) > MAX_TOTAL_BYTES;
}

export const MAX_TOTAL_UPLOAD_BYTES = MAX_TOTAL_BYTES;

/**
 * Top-level chips: root files are individual chips, each root folder is one
 * chip "имя папки · N файлов" (07-web-client.md).
 */
export interface Chip {
  key: string;
  kind: "file" | "folder";
  name: string;
  count: number;
  paths: string[];
}

export function chipsFromFiles(files: PickedFile[]): Chip[] {
  const chips: Chip[] = [];
  const byRoot = new Map<string, PickedFile[]>();
  for (const f of files) {
    const root = f.path.includes("/") ? (f.path.split("/")[0] ?? f.path) : f.path;
    const list = byRoot.get(root) ?? [];
    list.push(f);
    byRoot.set(root, list);
  }
  for (const [root, list] of byRoot) {
    const single = list.length === 1 ? list[0] : undefined;
    if (single && !single.path.includes("/")) {
      chips.push({ key: root, kind: "file", name: root, count: 1, paths: [single.path] });
    } else {
      chips.push({
        key: root,
        kind: "folder",
        name: root,
        count: list.length,
        paths: list.map((f) => f.path),
      });
    }
  }
  return chips;
}

/** Merges picked files into a map by path (new paths replace old ones). */
export function mergeFiles(existing: PickedFile[], added: PickedFile[]): PickedFile[] {
  const map = new Map(existing.map((f) => [f.path, f]));
  for (const f of added) {
    map.set(f.path, f);
  }
  return Array.from(map.values());
}

/** Removes every file under a chip (folder chip removes all its files). */
export function removeChip(files: PickedFile[], chip: Chip): PickedFile[] {
  const drop = new Set(chip.paths);
  return files.filter((f) => !drop.has(f.path));
}
