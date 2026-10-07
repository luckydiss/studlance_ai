import { DemoImage } from "./DemoImage";
import styles from "./Home.module.css";
import { ScaleFit } from "./ScaleFit";

// «Любые работы — готовым комплектом» — статичная секция Main.dc.html:
// текст слева, справа стопка из трёх листов с поворотами и тенями.

export function KitSection() {
  return (
    <section className={styles.kitSection}>
      <div className={styles.kitText}>
        <h2>Любые работы — готовым комплектом</h2>
        <p>
          Технические, гуманитарные, программирование: записка, расчёты, чертежи, код и презентация
          — в тех файлах, которые сдаются.
        </p>
        <p>Что-то не понравилось — отметьте место прямо на листе, и работу доработают.</p>
        <p className={styles.kitNote}>Листы из выполненных работ, личные данные скрыты.</p>
      </div>
      <ScaleFit width={860} height={420} className={styles.kitStackOuter}>
        <div
          className={styles.kitSheet}
          style={{ left: 0, top: 36, width: 480, height: 340, transform: "rotate(-2deg)" }}
        >
          <DemoImage src="/demo/stack-tech.jpg" alt="Лист чертежа А3 из технического проекта" />
        </div>
        <div
          className={styles.kitSheet}
          style={{ left: 430, top: 0, width: 250, height: 354, transform: "rotate(2.5deg)" }}
        >
          <DemoImage
            src="/demo/stack-code.jpg"
            alt="Страница записки со скриншотом запущенной программы"
          />
        </div>
        <div
          className={styles.kitSheet}
          style={{ left: 606, top: 56, width: 250, height: 354, transform: "rotate(-1.5deg)" }}
        >
          <DemoImage
            src="/demo/stack-hum.jpg"
            alt="Страница записки с таблицей и графиком из гуманитарной работы"
          />
        </div>
      </ScaleFit>
    </section>
  );
}
