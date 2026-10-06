// Formatting helpers shared by both frontends (07-web-client.md texts).

const MONTHS_GENITIVE = [
  "января",
  "февраля",
  "марта",
  "апреля",
  "мая",
  "июня",
  "июля",
  "августа",
  "сентября",
  "октября",
  "ноября",
  "декабря",
];

/** "4 октября, 14:20" (07-web-client.md, orders list). */
export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return "";
  }
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  return `${d.getDate()} ${MONTHS_GENITIVE[d.getMonth()]}, ${hh}:${mm}`;
}

/** Russian plural form: files(s), page(s). */
export function plural(n: number, one: string, few: string, many: string): string {
  const mod10 = n % 10;
  const mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) {
    return one;
  }
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 10 || mod100 >= 20)) {
    return few;
  }
  return many;
}

/** "12 КБ" / "3,4 МБ" style size for the file list. */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} Б`;
  }
  const kb = bytes / 1024;
  if (kb < 1024) {
    return `${Math.round(kb)} КБ`;
  }
  const mb = kb / 1024;
  if (mb < 1024) {
    return `${mb.toFixed(1).replace(".", ",")} МБ`;
  }
  return `${(mb / 1024).toFixed(1).replace(".", ",")} ГБ`;
}

/** "Заказ от 12 марта" (job page header date, KitWork mockup style). */
export function formatOrderDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return "";
  }
  return `Заказ от ${d.getDate()} ${MONTHS_GENITIVE[d.getMonth()]}`;
}
