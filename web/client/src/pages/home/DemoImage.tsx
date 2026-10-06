import styles from "./Home.module.css";

// Demo-картинки лежат вне публичного репозитория (docs/design/README.md):
// при ошибке загрузки img скрывается — остаётся белый лист того же размера,
// без «битой картинки» и без слома вёрстки.

interface DemoImageProps {
  src: string;
  alt: string;
}

export function DemoImage({ src, alt }: DemoImageProps) {
  return (
    <img
      src={src}
      alt={alt}
      className={styles.sheetImage}
      draggable={false}
      onError={(event) => {
        const image = event.currentTarget;
        image.removeAttribute("src");
        image.style.display = "none";
      }}
    />
  );
}
