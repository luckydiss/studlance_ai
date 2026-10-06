import { describe, expect, it } from "vitest";
import { formatBytes, formatDate, formatOrderDate, plural } from "./format";

// Date formatting is local-time, so tests derive expectations from the same
// Date object instead of hard-coding a timezone.

describe("formatDate", () => {
  it("renders '4 октября, 14:20' style", () => {
    const d = new Date(2026, 9, 4, 14, 20);
    const hh = String(d.getHours()).padStart(2, "0");
    const mm = String(d.getMinutes()).padStart(2, "0");
    expect(formatDate(d.toISOString())).toBe(`4 октября, ${hh}:${mm}`);
  });

  it("returns empty string for garbage", () => {
    expect(formatDate("not-a-date")).toBe("");
  });
});

describe("formatOrderDate", () => {
  it("renders 'Заказ от 12 марта'", () => {
    const d = new Date(2026, 2, 12, 9, 0);
    expect(formatOrderDate(d.toISOString())).toBe("Заказ от 12 марта");
  });
});

describe("plural", () => {
  it("picks russian forms", () => {
    expect(plural(1, "файл", "файла", "файлов")).toBe("файл");
    expect(plural(2, "файл", "файла", "файлов")).toBe("файла");
    expect(plural(5, "файл", "файла", "файлов")).toBe("файлов");
    expect(plural(11, "файл", "файла", "файлов")).toBe("файлов");
    expect(plural(21, "файл", "файла", "файлов")).toBe("файл");
  });
});

describe("formatBytes", () => {
  it("formats sizes", () => {
    expect(formatBytes(500)).toBe("500 Б");
    expect(formatBytes(2048)).toBe("2 КБ");
    expect(formatBytes(3.4 * 1024 * 1024)).toBe("3,4 МБ");
  });
});
