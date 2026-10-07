import type { Page, Remark } from "@studlance/shared";
import {
  type CSSProperties,
  type PointerEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  type Rect,
  type RectInput,
  isMeaningfulDrag,
  rectFromDrag,
  toFraction,
} from "../remarkMath";
import styles from "./Job.module.css";

export function rectangleStyle(rect: Rect): CSSProperties {
  return {
    left: `${rect.x * 100}%`,
    top: `${rect.y * 100}%`,
    width: `${rect.w * 100}%`,
    height: `${rect.h * 100}%`,
  };
}

export function PageCanvas({
  page,
  title,
  enabled,
  number,
  remarks,
  onAdd,
}: {
  page: Page;
  title: string;
  enabled: boolean;
  number: number;
  remarks: { key: string; remark: Remark; draft: boolean }[];
  onAdd: (rect: Rect, text: string) => void;
}) {
  const image = useRef<HTMLImageElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const drag = useRef<(RectInput & { pointer: number }) | null>(null);
  const [selection, setSelection] = useState<Rect | null>(null);
  const [pending, setPending] = useState(false);
  const [text, setText] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [broken, setBroken] = useState(false);
  const origin = useRef<HTMLElement | null>(null);

  const reset = useCallback(() => {
    drag.current = null;
    setSelection(null);
    setPending(false);
    setText("");
  }, []);

  const discard = useCallback(() => {
    if (drag.current && image.current?.hasPointerCapture(drag.current.pointer)) {
      image.current.releasePointerCapture(drag.current.pointer);
    }
    reset();
    origin.current?.focus({ preventScroll: true });
  }, [reset]);

  useEffect(() => {
    const onEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") discard();
    };
    window.addEventListener("keydown", onEscape);
    return () => window.removeEventListener("keydown", onEscape);
  }, [discard]);

  useEffect(() => {
    if (pending) textarea.current?.focus();
  }, [pending]);

  useEffect(() => {
    if (!enabled) reset();
  }, [enabled, reset]);

  function move(event: PointerEvent<HTMLImageElement>) {
    if (!drag.current || drag.current.pointer !== event.pointerId) return;
    const { fx, fy } = toFraction(
      event.clientX,
      event.clientY,
      event.currentTarget.getBoundingClientRect(),
    );
    drag.current = { ...drag.current, x1: fx, y1: fy };
    setSelection(rectFromDrag(drag.current));
  }

  function confirm() {
    if (!selection || !text.trim()) return;
    onAdd(selection, text.trim());
    reset();
  }

  return (
    <div className={styles.canvasArea}>
      <div className={`${styles.sheet} ${enabled ? styles.crosshair : ""}`}>
        <img
          ref={image}
          src={page.image_url}
          alt={`${title} · лист ${page.page}`}
          width={page.width}
          height={page.height}
          draggable={false}
          onLoad={() => {
            setLoaded(true);
            setBroken(false);
          }}
          onError={() => {
            setBroken(true);
            setLoaded(false);
          }}
          onPointerDown={(event) => {
            if (!enabled || pending || !loaded || !event.isPrimary || event.button !== 0) return;
            event.preventDefault();
            origin.current =
              document.activeElement instanceof HTMLElement ? document.activeElement : null;
            const { fx, fy } = toFraction(
              event.clientX,
              event.clientY,
              event.currentTarget.getBoundingClientRect(),
            );
            drag.current = { x0: fx, y0: fy, x1: fx, y1: fy, pointer: event.pointerId };
            event.currentTarget.setPointerCapture(event.pointerId);
            setSelection(null);
          }}
          onPointerMove={move}
          onPointerUp={(event) => {
            if (!drag.current || drag.current.pointer !== event.pointerId) return;
            move(event);
            const input = drag.current;
            const rect = event.currentTarget.getBoundingClientRect();
            const result = rectFromDrag(input);
            drag.current = null;
            if (event.currentTarget.hasPointerCapture(event.pointerId)) {
              event.currentTarget.releasePointerCapture(event.pointerId);
            }
            if (isMeaningfulDrag(input, 4, rect) && result.w > 0 && result.h > 0) {
              setSelection(result);
              setPending(true);
            } else {
              setSelection(null);
            }
          }}
          onPointerCancel={discard}
          onLostPointerCapture={() => {
            if (drag.current) reset();
          }}
        />
        {loaded &&
          remarks.map(({ key, remark, draft }) => (
            <span
              key={key}
              data-remark-frame={remark.idx}
              title={remark.text}
              className={`${styles.rectangle} ${draft ? styles.filled : ""}`}
              style={rectangleStyle(remark)}
            >
              <span className={styles.rectangleNumber}>{remark.idx}</span>
            </span>
          ))}
        {loaded && selection && enabled && (
          <span
            data-remark-frame={number}
            className={`${styles.rectangle} ${styles.filled}`}
            style={rectangleStyle(selection)}
          >
            <span className={styles.rectangleNumber}>{number}</span>
          </span>
        )}
        {pending && selection && enabled && (
          <div
            className={styles.popover}
            style={{
              left: `${Math.min(selection.x, 0.5) * 100}%`,
              top: `${Math.min(selection.y + selection.h, 0.65) * 100}%`,
            }}
          >
            <label htmlFor="remark-text">Что поправить?</label>
            <textarea
              id="remark-text"
              ref={textarea}
              value={text}
              onChange={(event) => {
                setText(event.target.value);
              }}
            />
            <div className={styles.actions}>
              <button
                type="button"
                className={styles.primary}
                disabled={!text.trim()}
                onClick={confirm}
              >
                Добавить
              </button>
              <button type="button" className={styles.secondary} onClick={discard}>
                Отмена
              </button>
            </div>
          </div>
        )}
      </div>
      {broken && <p role="alert">Не удалось загрузить страницу</p>}
    </div>
  );
}
