// Prints a coarse text heatmap of where the two screenshots differ, so the
// diff can be inspected without viewing images.
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const here = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.join(here, ".artifacts", "visual");

const toDataUrl = async (f) => {
  const buf = await readFile(f);
  return `data:image/png;base64,${buf.toString("base64")}`;
};

const browser = await chromium.launch();
const page = await browser.newPage();
const urlA = await toDataUrl(path.join(outDir, "home-1440.png"));
const urlB = await toDataUrl(path.join(outDir, "mockup-1440.png"));
const grid = await page.evaluate(
  async ([a, b]) => {
    const load = (src) =>
      new Promise((res, rej) => {
        const img = new Image();
        img.onload = () => res(img);
        img.onerror = () => rej(new Error("load failed"));
        img.src = src;
      });
    const imgA = await load(a);
    const imgB = await load(b);
    const w = Math.max(imgA.width, imgB.width);
    const h = Math.max(imgA.height, imgB.height);
    const c = document.createElement("canvas");
    c.width = w;
    c.height = h * 2;
    const ctx = c.getContext("2d", { willReadFrequently: true });
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, w, h * 2);
    ctx.drawImage(imgA, 0, 0);
    ctx.drawImage(imgB, 0, h);
    const A = ctx.getImageData(0, 0, w, h).data;
    const B = ctx.getImageData(0, h, w, h).data;
    // per-row change counts (compressed to bands of 20px)
    const rowBands = [];
    for (let y = 0; y < h; y += 20) {
      let n = 0;
      for (let yy = y; yy < Math.min(y + 20, h); yy += 1) {
        for (let x = 0; x < w; x += 1) {
          const i = (yy * w + x) * 4;
          const d =
            Math.abs(A[i] - B[i]) + Math.abs(A[i + 1] - B[i + 1]) + Math.abs(A[i + 2] - B[i + 2]);
          if (d > 24) n += 1;
        }
      }
      rowBands.push(n);
    }
    // column bands for selected row ranges
    const colBands = {};
    for (const [name, y0, y1] of [
      ["form", 100, 390],
      ["stack", 420, 880],
      ["carousel", 1260, 1660],
    ]) {
      const cols = [];
      for (let x = 0; x < w; x += 40) {
        let n = 0;
        for (let y = y0; y < y1; y += 1) {
          for (let xx = x; xx < Math.min(x + 40, w); xx += 1) {
            const i = (y * w + xx) * 4;
            const d =
              Math.abs(A[i] - B[i]) + Math.abs(A[i + 1] - B[i + 1]) + Math.abs(A[i + 2] - B[i + 2]);
            if (d > 24) n += 1;
          }
        }
        cols.push(n);
      }
      colBands[name] = { y0, y1, cols };
    }
    return { w, h, rowBands, colBands };
  },
  [urlA, urlB],
);
await browser.close();

const { w, h, rowBands } = grid;
const max = Math.max(...rowBands);
const scale = Math.max(1, Math.round(max / 40));
console.log(`image ${w}x${h}, band=20px, scale: 1 char ~ ${scale} px`);
for (const [name, band] of Object.entries(grid.colBands)) {
  const max = Math.max(...band.cols);
  console.log(`
[${name}] y=${band.y0}-${band.y1}, max col band=${max}`);
  band.cols.forEach((n, i) => {
    if (n > 0) {
      const bar = "#".repeat(Math.max(1, Math.round((n / Math.max(1, max)) * 30)));
      console.log(
        `  x=${String(i * 40).padStart(4)}-${String(i * 40 + 39).padStart(4)} ${String(n).padStart(6)} ${bar}`,
      );
    }
  });
}
rowBands.forEach((n, i) => {
  if (n > 0) {
    const bar = "#".repeat(Math.max(1, Math.round(n / scale)));
    console.log(
      `y=${String(i * 20).padStart(4)}-${String(i * 20 + 19).padStart(4)} ${String(n).padStart(6)} ${bar}`,
    );
  }
});
