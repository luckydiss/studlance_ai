import { type ReactNode, useLayoutEffect, useRef, useState } from "react";
import styles from "./Home.module.css";

// Стопка листов «Любые работы» и сцены слайдов имеют фиксированные размеры
// из Main.dc.html. Когда контейнер уже (мобильный), блок масштабируется
// целиком: transform: scale по ширине контейнера, высота обёртки —
// масштабированная высота (07-web-client.md, адаптивность).

interface ScaleFitProps {
  /** ширина блока в координатах макета */
  width: number;
  /** высота блока в координатах макета */
  height: number;
  /** центрировать блок, когда контейнер шире (этапы 3–4) */
  centered?: boolean;
  className?: string;
  children: ReactNode;
}

export function ScaleFit({ width, height, centered = false, className, children }: ScaleFitProps) {
  const outer = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(1);

  useLayoutEffect(() => {
    const element = outer.current;
    if (!element) return;
    const measure = () => setScale(Math.min(1, element.clientWidth / width));
    measure();
    if (typeof ResizeObserver === "undefined") {
      window.addEventListener("resize", measure);
      return () => window.removeEventListener("resize", measure);
    }
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [width]);

  return (
    <div ref={outer} className={className}>
      <div
        className={styles.scaleBox}
        style={{
          width: Math.round(width * scale),
          height: Math.round(height * scale),
          marginLeft: centered ? "auto" : undefined,
          marginRight: centered ? "auto" : undefined,
        }}
      >
        <div
          className={styles.scaleContent}
          style={{ width, height, transform: `scale(${scale})` }}
        >
          {children}
        </div>
      </div>
    </div>
  );
}
