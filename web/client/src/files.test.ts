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

it("preserves spaces in file and folder names byte-for-byte", () => {
  // Two names differing only by spaces are two different files (Windows
  // allows both); normalization must not collapse them.
  expect(normalizeRelPath("  report.txt")).toBe("  report.txt");
  expect(normalizeRelPath("report.txt")).toBe("report.txt");
  expect(normalizeRelPath("  report.txt")).not.toBe(normalizeRelPath("report.txt"));
  expect(normalizeRelPath("Папка с пробелами/ файл.txt ")).toBe("Папка с пробелами/ файл.txt ");
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

  it("keeps names that differ only by spaces as separate files", () => {
    const merged = mergeFiles([], [f("  report.txt"), f("report.txt")]);
    expect(merged).toHaveLength(2);
    const paths = merged.map((x) => x.path);
    expect(paths).toContain("  report.txt");
    expect(paths).toContain("report.txt");
  });

  it("replaces only the exactly matching path", () => {
    const first = { path: "  report.txt", file: new File(["один"], "report.txt") };
    const second = { path: "  report.txt", file: new File(["другие байты"], "report.txt") };
    const merged = mergeFiles([first], [second]);
    expect(merged).toHaveLength(1);
    expect(merged[0]?.file).toBe(second.file);
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
