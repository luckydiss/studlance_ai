import { useEffect, useMemo, useState } from "react";
import {
  type CarouselState,
  advance,
  createClock,
  jumpTo,
  prefersReducedMotion,
} from "../../carouselState";
import { DemoImage } from "./DemoImage";
import styles from "./Home.module.css";
import { ScaleFit } from "./ScaleFit";

// «Как это работает» — карусель-сторис из 5 этапов по 4 с (07-web-client.md,
// Main.dc.html). ОДИН таймер (createClock + advance) управляет и индексом
// слайда, и полосками прогресса; клик по полоске — jumpTo. Пауза при hover
// и при фокусе внутри; prefers-reduced-motion — без автопрокрутки и без
// CSS-перехода.

const STAGE_TITLES = [
  "Загрузили исходники",
  "ИИ сделал работу",
  "Вы проверили",
  "Что-то не так — выделили на листе",
  "Готовый комплект",
] as const;

function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(prefersReducedMotion);
  useEffect(() => {
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReduced(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);
  return reduced;
}

/** Прошедшие этапы залиты целиком, текущий — по прогрессу, будущие — пустые. */
function barFill(index: number, state: CarouselState): number {
  if (index < state.index) {
    return 1;
  }
  if (index === state.index) {
    return state.progress;
  }
  return 0;
}

export function HowCarousel() {
  const [state, setState] = useState<CarouselState>({ index: 0, progress: 0 });
  const [hovered, setHovered] = useState(false);
  const [focused, setFocused] = useState(false);
  const reduced = useReducedMotion();
  const clock = useMemo(() => createClock((dt) => setState((prev) => advance(prev, dt))), []);
  const paused = hovered || focused || reduced;

  useEffect(() => {
    if (paused) {
      return;
    }
    clock.start();
    return () => clock.stop();
  }, [paused, clock]);

  return (
    <section className={styles.howSection}>
      <div className={styles.howContainer}>
        <h2>Как это работает</h2>
        <section
          className={styles.carousel}
          aria-label="Как это работает"
          data-stage-active={state.index}
          onMouseEnter={() => setHovered(true)}
          onMouseLeave={() => setHovered(false)}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
        >
          <div className={styles.stageBars}>
            {STAGE_TITLES.map((title, index) => (
              <button
                key={title}
                type="button"
                className={styles.stageBar}
                aria-label={`Этап ${index + 1}: ${title}`}
                aria-current={index === state.index ? "true" : undefined}
                onClick={() => setState(jumpTo(index))}
              >
                <span
                  className={styles.stageBarFill}
                  style={{ transform: `scaleX(${barFill(index, state)})` }}
                />
              </button>
            ))}
          </div>
          <div
            className={`${styles.carouselTrack} ${reduced ? styles.noTransition : ""}`}
            style={{ transform: `translateX(-${state.index * 20}%)` }}
          >
            <div className={styles.slide}>
              <div className={styles.stageText}>
                <span className={styles.stageNum}>1 из 5</span>
                <h3 className={styles.stageTitle}>Загрузили исходники</h3>
                <p className={styles.stageBody}>
                  Пишете запрос своими словами и прикладываете всё, что есть: задание, методичку,
                  вариант.
                </p>
              </div>
              <div className={styles.stageFluid}>
                <div className={styles.replica}>
                  <div className={styles.replicaText}>
                    Курсовая работа, вариант 14. Всё по методичке кафедры, задание и методичка во
                    вложении.
                  </div>
                  <div className={styles.replicaChips}>
                    <span className={styles.replicaChip}>задание.pdf</span>
                    <span className={styles.replicaChip}>методичка.pdf</span>
                    <span className={styles.replicaChip}>вариант 14.jpg</span>
                  </div>
                  <div className={styles.replicaActions}>
                    <span className={styles.replicaSubmit}>Оформить заказ</span>
                  </div>
                </div>
              </div>
            </div>
            <div className={styles.slide}>
              <div className={styles.stageText}>
                <span className={styles.stageNum}>2 из 5</span>
                <h3 className={styles.stageTitle}>ИИ сделал работу</h3>
                <p className={styles.stageBody}>
                  Записка, расчёты, чертежи, код — весь комплект в тех файлах, которые сдаются.
                </p>
              </div>
              <ScaleFit width={620} height={380} className={styles.stageFit}>
                <div
                  className={styles.sheet}
                  style={{ left: 0, top: 60, width: 400, height: 283, transform: "rotate(-2deg)" }}
                >
                  <DemoImage
                    src="/demo/how-scheme-a3.jpg"
                    alt="Функциональная схема автоматизации, лист А3"
                  />
                </div>
                <div
                  className={styles.sheet}
                  style={{
                    left: 300,
                    top: 18,
                    width: 190,
                    height: 269,
                    transform: "rotate(2.5deg)",
                  }}
                >
                  <DemoImage
                    src="/demo/how-page-table.jpg"
                    alt="Страница записки с графиком переходного процесса"
                  />
                </div>
                <div
                  className={styles.sheet}
                  style={{
                    left: 412,
                    top: 92,
                    width: 190,
                    height: 269,
                    transform: "rotate(-1.5deg)",
                  }}
                >
                  <DemoImage
                    src="/demo/how-page-calc.jpg"
                    alt="Страница записки с тепловым расчётом"
                  />
                </div>
              </ScaleFit>
            </div>
            <div className={styles.slide}>
              <div className={styles.stageText}>
                <span className={styles.stageNum}>3 из 5</span>
                <h3 className={styles.stageTitle}>Вы проверили</h3>
                <p className={styles.stageBody}>
                  Листаете работу прямо на сайте, страница за страницей.
                </p>
              </div>
              <ScaleFit width={248} height={384} centered className={styles.stageFit}>
                <div className={styles.sheet} style={{ left: 0, top: 0, width: 248, height: 351 }}>
                  <DemoImage src="/demo/how-page-table.jpg" alt="Страница работы в просмотре" />
                </div>
                <span
                  className={styles.pageLabel}
                  style={{ left: 0, top: 363, width: 248, textAlign: "center" }}
                >
                  {"\u2039\u00a0\u00a0стр. 16 из 32\u00a0\u00a0\u203a"}
                </span>
              </ScaleFit>
            </div>
            <div className={styles.slide}>
              <div className={styles.stageText}>
                <span className={styles.stageNum}>4 из 5</span>
                <h3 className={styles.stageTitle}>Что-то не так — выделили на листе</h3>
                <p className={styles.stageBody}>
                  Выделяете место и пишете, что поправить. ИИ доработает.
                </p>
              </div>
              <ScaleFit width={434} height={384} centered className={styles.stageFit}>
                <div className={styles.sheet} style={{ left: 0, top: 0, width: 248, height: 351 }}>
                  <DemoImage
                    src="/demo/how-page-table.jpg"
                    alt="Страница работы с выделенным местом и замечанием"
                  />
                  <span className={styles.remarkBox} aria-hidden="true" />
                  <span className={styles.remarkPopover}>
                    Добавьте в таблицу время переходного процесса
                  </span>
                </div>
                <span
                  className={styles.pageLabel}
                  style={{ left: 0, top: 363, width: 248, textAlign: "center" }}
                >
                  стр. 16 из 32
                </span>
              </ScaleFit>
            </div>
            <div className={styles.slide}>
              <div className={styles.stageText}>
                <span className={styles.stageNum}>5 из 5</span>
                <h3 className={styles.stageTitle}>Готовый комплект</h3>
                <p className={styles.stageBody}>
                  Записка, расчёты, схемы и чертежи — новая версия с учтёнными замечаниями.
                  Скачиваете и сдаёте.
                </p>
                <span className={styles.downloadAll}>Скачать всё · версия 2</span>
              </div>
              <ScaleFit width={620} height={391} className={styles.stageFit}>
                <div className={styles.sheet} style={{ left: 0, top: 24, width: 250, height: 177 }}>
                  <DemoImage
                    src="/demo/how-scheme2-a3.jpg"
                    alt="Схема регулирования и защит, лист А3"
                  />
                </div>
                <div
                  className={styles.sheet}
                  style={{ left: 0, top: 214, width: 250, height: 177 }}
                >
                  <DemoImage
                    src="/demo/how-scheme-a3.jpg"
                    alt="Функциональная схема автоматизации, лист А3"
                  />
                </div>
                <div
                  className={styles.sheet}
                  style={{ left: 290, top: 34, width: 200, height: 283 }}
                >
                  <DemoImage
                    src="/demo/how-page-calc.jpg"
                    alt="Страница записки с тепловым расчётом"
                  />
                </div>
                <div
                  className={styles.sheet}
                  style={{ left: 318, top: 62, width: 200, height: 283 }}
                >
                  <DemoImage
                    src="/demo/how-page-table.jpg"
                    alt="Исправленная страница записки, версия 2"
                  />
                </div>
              </ScaleFit>
            </div>
          </div>
        </section>
      </div>
    </section>
  );
}
