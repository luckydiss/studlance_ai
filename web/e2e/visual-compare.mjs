// Visual acceptance for PR 5 (09-tasks.md): a labeled side-by-side of `/`
// and the Main.dc.html mockup at 1440 px, a separate heatmap with statistics,
// and mobile 390 px screenshots of `/` and a job page.
//
// The comparator is self-checked first (identical images → 0 changed pixels,
// different images → > 0, different sizes must not produce a false zero).
// It compares the full buffers over the union of both sizes — no cropping,
// no rescaling; missing pixels count as white.
//
// Demo images are absent on BOTH sides (they are intentionally not
// committed): each side shows the white-sheet fallback. The product page is
// brought into the mockup's initial state (same textarea text, same three
// file chips, carousel reset to stage 1); the mockup itself is not edited —
// only the x-dc/helmet render shim is added.

import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { startHarness } from "./harness.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../..");
const outDir = path.join(here, ".artifacts", "visual");

const MOCKUP_TEXT =
  "Курсовая работа, вариант 14. Всё по методичке кафедры, задание и методичка во вложении.";
const CHIP_FILES = ["задание.pdf", "методичка.pdf", "вариант 14.jpg"];

async function fontsSettled(page) {
  await page.evaluate(() => document.fonts.ready);
  const ok = await page.evaluate(() => ({
    golos600: document.fonts.check('600 18px "Golos Text"'),
    golos400: document.fonts.check('400 15px "Golos Text"'),
  }));
  if (!ok.golos600 || !ok.golos400) {
    throw new Error(`fonts not loaded: ${JSON.stringify(ok)}`);
  }
}

/**
 * Comparison core. Draws both images (data URLs) in the page, diffs every
 * pixel over the union size and, when `mount`, appends two elements to
 * #out: a labeled side-by-side canvas and a heatmap canvas. Returns stats.
 */
async function compare(page, urlA, urlB, { mount = false, labelA = "", labelB = "" } = {}) {
  return page.evaluate(
    async ([a, b, mount, labelA, labelB]) => {
      const load = (src) =>
        new Promise((res, rej) => {
          const img = new Image();
          img.onload = () => res(img);
          img.onerror = () => rej(new Error("img load failed"));
          img.src = src;
        });
      const imgA = await load(a);
      const imgB = await load(b);
      const w = Math.max(imgA.width, imgB.width);
      const h = Math.max(imgA.height, imgB.height);

      // Two scratch canvases at exact source positions to read pixels from.
      const scratch = document.createElement("canvas");
      scratch.width = w;
      scratch.height = h * 2;
      const sctx = scratch.getContext("2d", { willReadFrequently: true });
      sctx.fillStyle = "#ffffff";
      sctx.fillRect(0, 0, w, h * 2);
      sctx.drawImage(imgA, 0, 0);
      sctx.drawImage(imgB, 0, h);
      const dataA = sctx.getImageData(0, 0, w, h).data;
      const dataB = sctx.getImageData(0, h, w, h).data;

      const diff = document.createElement("canvas");
      diff.width = w;
      diff.height = h;
      const dctx = diff.getContext("2d");
      const image = dctx.createImageData(w, h);
      let changed = 0;
      for (let i = 0; i < w * h * 4; i += 4) {
        const d =
          Math.abs(dataA[i] - dataB[i]) +
          Math.abs(dataA[i + 1] - dataB[i + 1]) +
          Math.abs(dataA[i + 2] - dataB[i + 2]);
        if (d > 24) {
          changed += 1;
          image.data[i] = 227;
          image.data[i + 1] = 89;
          image.data[i + 2] = 12;
          image.data[i + 3] = 255;
        } else {
          image.data[i] = 235;
          image.data[i + 1] = 235;
          image.data[i + 2] = 235;
          image.data[i + 3] = 255;
        }
      }
      dctx.putImageData(image, 0, 0);

      if (mount) {
        const labelH = 44;
        const side = document.createElement("canvas");
        side.width = w;
        side.height = (h + labelH) * 2;
        const c = side.getContext("2d");
        c.fillStyle = "#ffffff";
        c.fillRect(0, 0, side.width, side.height);
        c.fillStyle = "#15171a";
        c.textBaseline = "middle";
        c.font = '600 22px "Golos Text", system-ui, sans-serif';
        c.fillText(labelA, 12, 22);
        c.fillText(labelB, 12, h + labelH + 22);
        c.drawImage(imgA, 0, labelH);
        c.drawImage(imgB, 0, h + labelH * 2);
        side.id = "sideBySide";
        diff.id = "heatmap";
        const out = document.getElementById("out");
        out.style.width = `${w}px`;
        out.appendChild(side);
        out.appendChild(diff);
      }
      const sizesMatch = imgA.width === imgB.width && imgA.height === imgB.height;
      // Equality requires both a zero pixel diff AND matching source
      // dimensions: a zero color diff over different sizes (e.g. white
      // images with a white tail) is still a mismatch.
      const equal = changed === 0 && sizesMatch;
      return {
        changed,
        total: w * h,
        sizesMatch,
        equal,
        widthA: imgA.width,
        heightA: imgA.height,
        widthB: imgB.width,
        heightB: imgB.height,
      };
    },
    [urlA, urlB, mount, labelA, labelB],
  );
}

/** Solid-color data-URL images for the comparator self-checks. */
async function synthetic(page, w, h, rgb) {
  return page.evaluate(
    ([w2, h2, color]) => {
      const c = document.createElement("canvas");
      c.width = w2;
      c.height = h2;
      const ctx = c.getContext("2d");
      ctx.fillStyle = `rgb(${color[0]},${color[1]},${color[2]})`;
      ctx.fillRect(0, 0, w2, h2);
      return c.toDataURL("image/png");
    },
    [w, h, rgb],
  );
}

async function selfCheck(page) {
  const red = await synthetic(page, 64, 48, [200, 30, 30]);
  const redAgain = await synthetic(page, 64, 48, [200, 30, 30]);
  const blue = await synthetic(page, 64, 48, [30, 30, 200]);
  const white = await synthetic(page, 64, 48, [255, 255, 255]);
  const whiteWider = await synthetic(page, 72, 48, [255, 255, 255]);
  const whiteTaller = await synthetic(page, 64, 56, [255, 255, 255]);
  const redWider = await synthetic(page, 72, 48, [200, 30, 30]);

  const same = await compare(page, red, redAgain);
  if (!same.equal || same.changed !== 0) {
    throw new Error(`self-check failed: identical images -> ${same.changed}, equal=${same.equal}`);
  }
  const differs = await compare(page, red, blue);
  if (differs.equal || differs.changed !== differs.total || differs.changed === 0) {
    throw new Error(`self-check failed: different images -> ${differs.changed}/${differs.total}`);
  }
  // White images of different sizes: the color diff over the union is zero,
  // but the comparison must still report a mismatch (no false equality).
  const whiteWidth = await compare(page, white, whiteWider);
  if (whiteWidth.equal) {
    throw new Error("self-check failed: white width mismatch reported equal");
  }
  if (whiteWidth.sizesMatch) {
    throw new Error("self-check failed: white width mismatch not detected");
  }
  const whiteHeight = await compare(page, white, whiteTaller);
  if (whiteHeight.equal) {
    throw new Error("self-check failed: white height mismatch reported equal");
  }
  if (whiteHeight.sizesMatch) {
    throw new Error("self-check failed: white height mismatch not detected");
  }
  // Colored size mismatch keeps a nonzero pixel diff too.
  const colored = await compare(page, red, redWider);
  if (colored.equal || colored.changed === 0) {
    throw new Error("self-check failed: colored size mismatch produced a zero diff");
  }
  return {
    same: { changed: same.changed, equal: same.equal },
    different: differs.changed,
    whiteWidthMismatch: { changed: whiteWidth.changed, equal: whiteWidth.equal },
    whiteHeightMismatch: { changed: whiteHeight.changed, equal: whiteHeight.equal },
    coloredSizeMismatch: colored.changed,
  };
}

async function toDataUrl(file) {
  const buf = await readFile(file);
  return `data:image/png;base64,${buf.toString("base64")}`;
}

async function main() {
  await mkdir(outDir, { recursive: true });
  const harness = await startHarness();
  const baseUrl = harness.baseUrl;
  const browser = await chromium.launch();
  try {
    // Comparator self-checks on synthetic images.
    const work = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await work.setContent("<div id='out'></div><style>body{margin:0}</style>");
    const checks = await selfCheck(work);
    console.log("comparator self-checks:", JSON.stringify(checks));

    // --- the product page in the mockup's initial state ---
    const page = await browser.newPage({ viewport: { width: 1440, height: 1660 } });
    await page.goto(`${baseUrl}/login`);
    await page.getByLabel("Почта").fill("client@example.com");
    await page.getByLabel("Пароль").fill("client-password-123");
    await page.getByRole("button", { name: "Войти" }).click();
    await page.getByRole("heading", { name: "Что нужно сделать?" }).waitFor();
    await page.goto(`${baseUrl}/`);
    await page.getByRole("textbox", { name: "Запрос" }).fill(MOCKUP_TEXT);
    await page.locator("#order-files").setInputFiles(
      CHIP_FILES.map((name) => ({
        name,
        mimeType: "application/octet-stream",
        buffer: Buffer.from("выдуманный тестовый файл"),
      })),
    );
    await page.getByText("вариант 14.jpg").first().waitFor();
    // Freeze the carousel for the snapshot by jumping to stage 1 (progress
    // resets to 0); the running animation itself is not modified. Wait out
    // the 0.6 s slide transition so the shot is not mid-motion.
    await page.getByRole("button", { name: "Этап 1: Загрузили исходники" }).click();
    await fontsSettled(page);
    await page.waitForTimeout(900);
    // Snapshot fixation only: a transform layer rasterizes text differently
    // than a plain one, so both sides are shot with the track at the identity
    // transform (visually the same state — stage 1, translateX(0)). The
    // product animation itself is not modified.
    await page.evaluate(() => {
      const carousel = document.querySelector("[data-stage-active]");
      const track = Array.from(carousel.querySelectorAll("div")).find(
        (d) => d.getBoundingClientRect().width > 4000,
      );
      if (!track) {
        throw new Error("carousel track not found");
      }
      track.style.transform = "none";
    });
    const homeShot = path.join(outDir, "home-1440.png");
    await page.screenshot({ path: homeShot, fullPage: true });

    // --- the mockup: render shim only, no content edits ---
    const mockShot = path.join(outDir, "mockup-1440.png");
    await page.goto(
      `file://${path.join(repoRoot, "docs/design/mockups", "Main.dc.html").replaceAll("\\", "/")}`,
    );
    await page.addStyleTag({ content: "x-dc{display:block}helmet{display:none}" });
    // The same white-sheet fallback as the product: /demo images are absent
    // on both sides, and a raw <img> with a failed src would render a
    // broken-image glyph instead of a clean sheet.
    await page.evaluate(() => {
      for (const img of Array.from(document.querySelectorAll("img"))) {
        const hide = () => {
          img.style.visibility = "hidden";
        };
        if (img.complete && img.naturalWidth === 0) {
          hide();
        } else {
          img.addEventListener("error", hide);
        }
      }
      // Snapshot fixation: stop every sl-swipe/segN animation (the track AND
      // the progress-bar fills) at their start state and drop the transform,
      // so both sides show stage 1 with progress 0. Only the render wrapper
      // is touched; the mockup file is not modified.
      const track = document.querySelector("div[style*='sl-swipe']");
      track.style.animation = "none";
      track.style.transform = "none";
      // The progress-bar fills (seg1..seg5) also carry .sl-anim: stop them at
      // their base scaleX(0). The track itself is excluded — scaleX(0) would
      // collapse it.
      for (const anim of Array.from(document.querySelectorAll(".sl-anim"))) {
        if (anim === track || anim.getBoundingClientRect().width > 1000) {
          continue;
        }
        anim.style.animation = "none";
        anim.style.transform = "scaleX(0)";
      }
    });
    await fontsSettled(page);
    await page.waitForTimeout(400);
    await page.screenshot({ path: mockShot, fullPage: true });
    await page.close();

    // --- comparison over the full buffers ---
    const stats = await compare(work, await toDataUrl(homeShot), await toDataUrl(mockShot), {
      mount: true,
      labelA: "Реализация · / · 1440px · demo-картинки отсутствуют (белые листы)",
      labelB: "Макет · Main.dc.html · 1440px · demo-картинки отсутствуют (белые листы)",
    });
    await work.locator("#sideBySide").screenshot({ path: path.join(outDir, "compare.png") });
    await work.locator("#heatmap").screenshot({ path: path.join(outDir, "diff.png") });
    const summary = {
      changed: stats.changed,
      total: stats.total,
      percent: Number(((stats.changed / stats.total) * 100).toFixed(3)),
      sizesMatch: stats.sizesMatch,
      equal: stats.equal,
      widthA: stats.widthA,
      heightA: stats.heightA,
      widthB: stats.widthB,
      heightB: stats.heightB,
    };
    await writeFile(path.join(outDir, "stats.json"), JSON.stringify(summary, null, 2), "utf8");
    console.log("comparison stats:", JSON.stringify(summary));
    await work.close();

    // --- mobile screenshots: / and a finished job at 390 px ---
    const login = await fetch(`${baseUrl}/api/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: "client@example.com", password: "client-password-123" }),
    });
    const cookie = /sl_session=[^;]+/.exec(login.headers.get("set-cookie") ?? "")?.[0];
    const mobileContext = await browser.newContext({ viewport: { width: 390, height: 844 } });
    if (cookie) {
      const sessionValue = /sl_session=([^;]+)/.exec(cookie)?.[1];
      if (sessionValue) {
        await mobileContext.addCookies([{ name: "sl_session", value: sessionValue, url: baseUrl }]);
      }
    }
    const mobile = await mobileContext.newPage();
    await mobile.goto(`${baseUrl}/`);
    await fontsSettled(mobile);
    await mobile.waitForTimeout(300);
    await mobile.screenshot({ path: path.join(outDir, "home-390.png"), fullPage: true });

    await fetch(`${baseUrl}/api/client/jobs`, {
      method: "POST",
      headers: { Cookie: cookie, "Content-Type": "application/json" },
      body: JSON.stringify({ prompt: "Заказ для мобильного скриншота страницы заказа" }),
    });
    const list = await (
      await fetch(`${baseUrl}/api/client/jobs`, { headers: { Cookie: cookie } })
    ).json();
    const jobId = list.jobs.find((j) => j.title.startsWith("Заказ для мобильного")).id;
    await fetch(
      `${baseUrl}/api/client/jobs/${jobId}/input?path=${encodeURIComponent("задание.pdf")}`,
      {
        method: "PUT",
        headers: { Cookie: cookie, "Content-Type": "application/octet-stream" },
        body: "исходники",
      },
    );
    await fetch(`${baseUrl}/api/client/jobs/${jobId}/submit`, {
      method: "POST",
      headers: { Cookie: cookie },
    });
    const deadline = Date.now() + 180_000;
    for (;;) {
      const job = await (
        await fetch(`${baseUrl}/api/client/jobs/${jobId}`, { headers: { Cookie: cookie } })
      ).json();
      if (job.client_status === "done") {
        break;
      }
      if (Date.now() > deadline) {
        throw new Error(`job not done: ${job.client_status}`);
      }
      await new Promise((r) => setTimeout(r, 500));
    }
    await mobile.goto(`${baseUrl}/orders/${jobId}`);
    await mobile.getByText("Пояснительная записка").first().waitFor();
    await mobile.waitForTimeout(500);
    await mobile.screenshot({ path: path.join(outDir, "job-390.png"), fullPage: true });
    await mobileContext.close();

    console.log(`visual artifacts: ${outDir}`);
  } finally {
    await browser.close();
    await harness.stop();
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
