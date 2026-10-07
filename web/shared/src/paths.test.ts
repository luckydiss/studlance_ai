import { describe, expect, it } from "vitest";
import { bundleUrl, inputUploadUrl, jobStreamUrl, safeNextPath } from "./paths";

describe("safeNextPath", () => {
  it("keeps local absolute paths", () => {
    expect(safeNextPath("/orders/abc")).toBe("/orders/abc");
    expect(safeNextPath("/")).toBe("/");
  });

  it("falls back to / for external or malformed values", () => {
    expect(safeNextPath(null)).toBe("/");
    expect(safeNextPath("")).toBe("/");
    expect(safeNextPath("http://evil.example")).toBe("/");
    expect(safeNextPath("//evil.example/x")).toBe("/");
    expect(safeNextPath("/\\evil")).toBe("/");
    expect(safeNextPath("orders/abc")).toBe("/");
    expect(safeNextPath("javascript:alert(1)")).toBe("/");
    expect(safeNextPath("/url:https://evil")).toBe("/");
    expect(safeNextPath("C:/windows")).toBe("/");
  });
});

describe("inputUploadUrl", () => {
  it("encodes the path exactly once", () => {
    expect(inputUploadUrl("j1", "задание.pdf")).toBe(
      "/api/client/jobs/j1/input?path=%D0%B7%D0%B0%D0%B4%D0%B0%D0%BD%D0%B8%D0%B5.pdf",
    );
  });

  it("keeps slashes inside a nested path encoded once", () => {
    const url = inputUploadUrl("j 1", "методичка/примеры/задание.pdf");
    expect(url).toContain("/api/client/jobs/j%201/input?path=");
    expect(url).not.toContain("%25"); // no double encoding
    const query = url.split("path=")[1] ?? "";
    expect(decodeURIComponent(query)).toBe("методичка/примеры/задание.pdf");
  });
});

describe("other urls", () => {
  it("builds stream and bundle urls", () => {
    expect(jobStreamUrl("abc")).toBe("/api/client/jobs/abc/stream");
    expect(bundleUrl("abc", 2)).toBe("/api/client/jobs/abc/versions/2/bundle.zip");
  });
});
