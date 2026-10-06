import { plural, useToast } from "@studlance/shared";
import { useId, useState } from "react";
import {
  type PickedFile,
  chipsFromFiles,
  filesFromDataTransfer,
  filesFromInput,
  removeChip,
} from "../files";
import styles from "./Job.module.css";
import { useJobError } from "./api";

export function FilePicker({
  onAdd,
  disabled = false,
  label = "Прикрепить файлы или папку",
  files,
  onRemove,
  onReadingChange,
}: {
  onAdd: (files: PickedFile[]) => void;
  disabled?: boolean;
  label?: string;
  files?: PickedFile[];
  onRemove?: (files: PickedFile[]) => void;
  onReadingChange?: (reading: boolean) => void;
}) {
  const id = useId();
  const [menu, setMenu] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [reading, setReading] = useState(false);
  const errorToast = useJobError();
  const { show } = useToast();
  const blocked = disabled || reading;

  function updateReading(value: boolean) {
    setReading(value);
    onReadingChange?.(value);
  }

  return (
    <section
      className={`${styles.dropZone} ${dragging ? styles.dragging : ""}`}
      aria-label={label}
      onDragOver={(event) => {
        if (!event.dataTransfer.types.includes("Files")) return;
        event.preventDefault();
        if (!blocked) setDragging(true);
      }}
      onDragLeave={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDragging(false);
      }}
      onDrop={(event) => {
        event.preventDefault();
        setDragging(false);
        if (blocked) return;
        updateReading(true);
        void filesFromDataTransfer(event.dataTransfer)
          .then(onAdd)
          .catch(errorToast)
          .finally(() => updateReading(false));
      }}
    >
      <button
        type="button"
        className={styles.secondary}
        aria-expanded={menu}
        aria-controls={`${id}-menu`}
        aria-haspopup="menu"
        disabled={blocked}
        onClick={() => {
          setMenu(!menu);
        }}
      >
        {label}
      </button>
      {menu && (
        <div
          id={`${id}-menu`}
          className={styles.fileMenu}
          role="menu"
          aria-label={label}
          onKeyDown={(event) => {
            if (event.key === "Escape") setMenu(false);
          }}
          onBlur={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget)) setMenu(false);
          }}
        >
          <label className={styles.fileLabel} htmlFor={`${id}-files`}>
            Файлы
          </label>
          <input
            id={`${id}-files`}
            aria-label="Файлы"
            type="file"
            multiple
            hidden
            disabled={blocked}
            onChange={(event) => {
              onAdd(filesFromInput(event.currentTarget.files));
              event.currentTarget.value = "";
              setMenu(false);
            }}
          />
          <label className={styles.fileLabel} htmlFor={`${id}-folder`}>
            Папку
          </label>
          <input
            id={`${id}-folder`}
            aria-label="Папку"
            type="file"
            multiple
            hidden
            {...{ webkitdirectory: "" }}
            disabled={blocked}
            onChange={(event) => {
              onAdd(filesFromInput(event.currentTarget.files));
              event.currentTarget.value = "";
              setMenu(false);
            }}
          />
        </div>
      )}
      <p className={styles.muted}>{label}</p>
      {files && (
        <div className={styles.chips}>
          {chipsFromFiles(files).map((chip) => (
            <span className={styles.chip} key={chip.key}>
              {chip.name}
              {chip.kind === "folder" &&
                ` · ${chip.count} ${plural(chip.count, "файл", "файла", "файлов")}`}
              <button
                type="button"
                aria-label={`Убрать ${chip.name}`}
                disabled={blocked}
                onClick={() => {
                  if (onRemove) onRemove(removeChip(files, chip));
                  else show("Не удалось убрать файл");
                }}
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}
    </section>
  );
}
