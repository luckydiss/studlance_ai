import { formatDate } from "@studlance/shared";
import { useEffect, useMemo, useRef, useState } from "react";
import type { AdminStep, StepMap } from "../adminJob";
import { stageLabel } from "../status";

// Left column (08-web-admin.md): agent runs in order with timing, outcome,
// tokens/cost and the raw-log link; inside, the merged HTTP+live steps with a
// type filter, on-demand payload and autoscroll that yields to the user.

export interface RunView {
  id: string;
  agent: string;
  stage: string;
  version: number;
  attempt: number;
  started_at: string;
  finished_at?: string | null;
  outcome?: string | null;
  input_tokens?: number | null;
  output_tokens?: number | null;
  cost_usd?: number | null;
  error?: string | null;
}

const TYPE_LABELS: Record<string, string> = {
  message: "сообщение",
  agent_message: "сообщение",
  reasoning: "рассуждение",
  command: "команда",
  command_execution: "команда",
  file: "файл",
  file_change: "файл",
  web: "веб",
  web_search: "веб",
  tool: "инструмент",
  error: "ошибка",
};

function typeLabel(type: string): string {
  return TYPE_LABELS[type] ?? type;
}

function duration(run: RunView): string | null {
  if (!run.finished_at) {
    return null;
  }
  const ms = new Date(run.finished_at).getTime() - new Date(run.started_at).getTime();
  if (!Number.isFinite(ms) || ms < 0) {
    return null;
  }
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) {
    return `${seconds} с`;
  }
  return `${Math.round(seconds / 60)} мин`;
}

function runTitle(run: RunView): string {
  const stage = stageLabel(run.stage) ?? run.stage;
  const version = run.version > 1 ? ` ${run.version}` : "";
  return `${run.agent} · ${stage}${version}`;
}

const KNOWN_TYPES = ["message", "reasoning", "command", "file", "web", "tool", "error"] as const;
const STEP_WINDOW = 200;

function normalizeType(type: string): string {
  if (type === "agent_message") return "message";
  if (type === "command_execution") return "command";
  if (type === "file_change") return "file";
  if (type === "web_search") return "web";
  return type;
}

export function TracePanel({
  jobId,
  runs,
  steps,
  historyError,
  historyLoading,
  onRetryHistory,
}: {
  jobId: string;
  runs: RunView[];
  steps: StepMap;
  historyError: unknown;
  historyLoading: boolean;
  onRetryHistory: () => void;
}) {
  const [filter, setFilter] = useState<string>("all");
  const [windowEnd, setWindowEnd] = useState<number | null>(null);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const listRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  const ordered = runs;
  const allSteps = useMemo(() => {
    const result: { run: RunView; step: AdminStep }[] = [];
    for (const run of ordered) {
      for (const step of steps[run.id] ?? []) {
        result.push({ run, step });
      }
    }
    return result;
  }, [ordered, steps]);

  const filtered = useMemo(
    () =>
      filter === "all"
        ? allSteps
        : allSteps.filter(({ step }) => normalizeType(step.type) === filter),
    [allSteps, filter],
  );
  const visibleStart =
    windowEnd === null
      ? Math.max(0, filtered.length - STEP_WINDOW)
      : Math.max(0, windowEnd - STEP_WINDOW);
  const visibleEnd = windowEnd === null ? filtered.length : Math.min(windowEnd, filtered.length);
  const visible = filtered.slice(visibleStart, visibleEnd);

  const lastSeq = visible.length > 0 ? visible[visible.length - 1]?.step.seq : undefined;

  // Autoscroll only while the user is at the bottom. `lastSeq` triggers the
  // effect when a new step arrives; it is not read in the body on purpose.
  // biome-ignore lint/correctness/useExhaustiveDependencies: lastSeq is the arrival trigger.
  useEffect(() => {
    const el = listRef.current;
    if (!el || !stick.current) {
      return;
    }
    el.scrollTop = el.scrollHeight;
  }, [lastSeq]);

  function onScroll() {
    const el = listRef.current;
    if (!el) {
      return;
    }
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    stick.current = atBottom;
  }

  function toggle(key: string) {
    setExpanded((previous) => {
      const next = new Set(previous);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  }

  return (
    <section className="sl-admin-trace" aria-label="Трейс агентов">
      <div className="sl-admin-trace-head">
        <h2>Действия агентов</h2>
        <label className="sl-admin-trace-filter">
          <span>Тип</span>
          <select
            value={filter}
            onChange={(event) => {
              setFilter(event.target.value);
              setWindowEnd(null);
              stick.current = true;
            }}
          >
            <option value="all">Все</option>
            {KNOWN_TYPES.map((type) => (
              <option key={type} value={type}>
                {typeLabel(type)}
              </option>
            ))}
          </select>
        </label>
      </div>

      <div className="sl-admin-runs">
        {ordered.map((run) => {
          const runSteps = steps[run.id] ?? [];
          const shown = visible.filter(({ run: r }) => r.id === run.id);
          const dur = duration(run);
          return (
            <div className="sl-admin-run" key={run.id}>
              <div className="sl-admin-run-head">
                <span
                  className={`sl-admin-run-dot sl-admin-run-dot--${run.agent}`}
                  aria-hidden="true"
                />
                <strong>{runTitle(run)}</strong>
                <span className="sl-admin-muted">
                  {formatDate(run.started_at)}
                  {dur ? ` · ${dur}` : ""} · {runSteps.length} шагов
                </span>
              </div>
              <div className="sl-admin-run-meta sl-admin-muted">
                {run.outcome ? <span>исход: {run.outcome}</span> : <span>исход: —</span>}
                {run.error ? <span className="sl-admin-error"> {run.error}</span> : null}
                <span>
                  {run.input_tokens != null || run.output_tokens != null
                    ? `токены: ${run.input_tokens ?? 0}/${run.output_tokens ?? 0}`
                    : "токены: —"}
                </span>
                <span>{run.cost_usd != null ? `$${run.cost_usd.toFixed(4)}` : "стоимость: —"}</span>
                <a href={`/api/admin/jobs/${jobId}/runs/${run.id}/log`}>сырой лог</a>
              </div>
              {filter !== "all" && shown.length === 0 ? (
                <p className="sl-admin-state sl-admin-state--inline">Нет шагов выбранного типа</p>
              ) : null}
            </div>
          );
        })}
      </div>

      {historyError ? (
        <div className="sl-admin-state sl-admin-state--inline" role="alert">
          Не удалось загрузить историю шагов. Проверьте соединение и повторите запрос.
          <button type="button" className="sl-admin-button" onClick={onRetryHistory}>
            Повторить
          </button>
        </div>
      ) : historyLoading ? (
        <output className="sl-admin-state sl-admin-state--inline">Загружаем историю…</output>
      ) : null}

      <div className="sl-admin-trace-page-controls">
        <button
          type="button"
          className="sl-admin-button sl-admin-button--ghost"
          disabled={visibleStart === 0}
          onClick={() => {
            stick.current = false;
            setWindowEnd(visibleStart);
          }}
        >
          Более ранние шаги
        </button>
        <span className="sl-admin-muted">
          {filtered.length === 0
            ? "0 шагов"
            : `${visibleStart + 1}–${visibleEnd} из ${filtered.length}`}
        </span>
        {windowEnd !== null && (
          <button
            type="button"
            className="sl-admin-button sl-admin-button--ghost"
            onClick={() => {
              const nextEnd = Math.min(filtered.length, windowEnd + STEP_WINDOW);
              if (nextEnd >= filtered.length) {
                setWindowEnd(null);
                stick.current = true;
              } else {
                setWindowEnd(nextEnd);
              }
            }}
          >
            Позже
          </button>
        )}
        {windowEnd !== null && (
          <button
            type="button"
            className="sl-admin-button sl-admin-button--ghost"
            onClick={() => {
              setWindowEnd(null);
              stick.current = true;
            }}
          >
            К последнему шагу
          </button>
        )}
      </div>
      <div
        className="sl-admin-steps"
        ref={listRef}
        onScroll={onScroll}
        data-step-count={filtered.length}
        aria-label="Шаги трейса"
      >
        {visible.length === 0 ? (
          <p className="sl-admin-state sl-admin-state--inline">Шагов пока нет</p>
        ) : (
          <ol className="sl-admin-step-list">
            {visible.map(({ run, step }) => {
              const key = `${run.id}:${step.seq}`;
              const open = expanded.has(key);
              return (
                <li className="sl-admin-step" key={key} data-seq={step.seq}>
                  <button
                    type="button"
                    className="sl-admin-step-row"
                    aria-expanded={open}
                    onClick={() => toggle(key)}
                  >
                    <span className="sl-admin-step-time">{formatDate(step.ts).split(" ")[1]}</span>
                    <span
                      className={`sl-admin-step-type sl-admin-step-type--${normalizeType(step.type)}`}
                    >
                      {typeLabel(step.type)}
                    </span>
                    <span className="sl-admin-step-summary">{step.summary}</span>
                  </button>
                  {open && step.payload != null && (
                    <pre className="sl-admin-step-payload">{renderPayload(step.payload)}</pre>
                  )}
                </li>
              );
            })}
          </ol>
        )}
      </div>
    </section>
  );
}

/** Payload is rendered as plain text, never as HTML (08-web-admin.md). */
function renderPayload(payload: unknown): string {
  if (typeof payload === "string") {
    return payload;
  }
  try {
    return JSON.stringify(payload, null, 2);
  } catch {
    return String(payload);
  }
}
