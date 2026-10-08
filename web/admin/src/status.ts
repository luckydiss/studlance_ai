// Job status vocabulary for the panel (08-web-admin.md, 03-lifecycle.md).

export type StatusGroup = "all" | "active" | "answer" | "done" | "failed" | "canceled";

export const STATUS_GROUP_LABELS: Record<StatusGroup, string> = {
  all: "Все",
  active: "В работе",
  answer: "Ждут ответа",
  done: "Готово",
  failed: "Сбой",
  canceled: "Отменены",
};

// The API takes one exact status; a group filter expands to the concrete
// values client-side (no invented contract values). "all" sends no status.
export const STATUS_GROUP_VALUES: Record<StatusGroup, string[]> = {
  all: [],
  active: ["queued", "running", "revising"],
  answer: ["needs_input"],
  done: ["done"],
  failed: ["failed"],
  canceled: ["canceled"],
};

export function isStatusGroup(value: string | null): value is StatusGroup {
  return (
    value === "all" ||
    value === "active" ||
    value === "answer" ||
    value === "done" ||
    value === "failed" ||
    value === "canceled"
  );
}

const STATUS_LABELS: Record<string, string> = {
  uploading: "Загрузка",
  queued: "В очереди",
  running: "В работе",
  revising: "Доработка",
  needs_input: "Ждёт ответа",
  done: "Готово",
  failed: "Сбой",
  canceled: "Отменён",
};

export function statusLabel(status: string): string {
  return STATUS_LABELS[status] ?? status;
}

const STAGE_LABELS: Record<string, string> = {
  draft: "черновик",
  verify: "проверка",
  revise: "доработка",
};

export function stageLabel(stage: string | null | undefined): string | null {
  if (!stage) {
    return null;
  }
  return STAGE_LABELS[stage] ?? stage;
}
