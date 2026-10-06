import { describe, expect, it } from "vitest";
import { type PickedFile, chipsFromFiles, mergeFiles, normalizeRelPath, removeChip } from "./files";

function f(path: string): PickedFile {
  return { path, file: { name: path.split("/").pop() ?? path } as File };
}

describe("normalizeRelPath", () => {
  it("keeps folder structure", () => {
    expect(normalizeRelPath("методичка/примеры/задание.pdf")).toBe("методичка/примеры/задание.pdf");
  });

  it("converts backslashes and drops traversal segments", () => {
    expect(normalizeRelPath("папка\\подпапка\\файл.txt")).toBe("папка/подпапка/файл.txt");
    expect(normalizeRelPath("../evil.txt")).toBe("evil.txt");
    expect(normalizeRelPath("./a/./b.txt")).toBe("a/b.txt");
  });
});

describe("chipsFromFiles", () => {
  it("groups folder files into one chip with a count", () => {
    const chips = chipsFromFiles([
      f("задание.pdf"),
      f("методичка/титул.pdf"),
      f("методичка/варианты/14.jpg"),
    ]);
    expect(chips).toHaveLength(2);
    const folder = chips.find((c) => c.kind === "folder");
    expect(folder?.name).toBe("методичка");
    expect(folder?.count).toBe(2);
    const file = chips.find((c) => c.kind === "file");
    expect(file?.name).toBe("задание.pdf");
  });

  it("treats a single nested file as a folder chip", () => {
    const chips = chipsFromFiles([f("папка/один.txt")]);
    expect(chips[0]?.kind).toBe("folder");
  });
});

describe("mergeFiles/removeChip", () => {
  it("replaces by path", () => {
    const merged = mergeFiles([f("a.txt")], [f("a.txt"), f("b.txt")]);
    expect(merged.map((x) => x.path)).toEqual(["a.txt", "b.txt"]);
  });

  it("removes all files of a folder chip", () => {
    const files = [f("задание.pdf"), f("методичка/a.pdf"), f("методичка/b.pdf")];
    const chip = chipsFromFiles(files).find((c) => c.kind === "folder");
    if (!chip) {
      throw new Error("no folder chip");
    }
    expect(removeChip(files, chip).map((x) => x.path)).toEqual(["задание.pdf"]);
  });
});
