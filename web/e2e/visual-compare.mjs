// Visual acceptance for PR 5 (09-tasks.md): a screenshot of `/` next to the
// Main.dc.html mockup at 1440 px, plus a same-size diff composite, plus
// mobile 390 px screenshots of `/` and the job page. Demo images are absent
// locally, so both sides show white sheets — that is the documented fallback.

import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { startHarness } from "./harness.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../..");
const outDir = path.join(here, ".artifacts", "visual");

async function main() {
  await mkdir(outDir, { recursive: true });
  const harness = await startHarness();
  const browser = await chromium.launch();
  try {
    const baseUrl = harness.baseUrl;
    const page = await browser.newPage({ viewport: { width: 1440, height: 1660 } });

    // Log in and open the home page.
    await page.goto(`${baseUrl}/login`);
    await page.getByLabel("Почта").fill("client@example.com");
    await page.getByLabel("Пароль").fill("client-password-123");
    await page.getByRole("button", { name: "Войти" }).click();
    await page.getByRole("heading", { name: "Что нужно сделать?" }).waitFor();
    await page.goto(`${baseUrl}/`);
    await page.waitForTimeout(1200); // fonts settle; carousel starts at stage 1
    await page.screenshot({ path: path.join(outDir, "home-1440.png"), fullPage: true });

    // The mockup: same viewport, x-dc/helmet shimmed so raw HTML renders.
    await page.goto(
      `file://${path.join(repoRoot, "docs/design/mockups", "Main.dc.html").replaceAll("\\", "/")}`,
    );
    await page.addStyleTag({ content: "x-dc{display:block}helmet{display:none}" });
    await page.evaluate(() => {
      const scroll = document.querySelector("x-dc > div");
      if (scroll) {
        scroll.style.minHeight = "";
      }
    });
    await page.waitForTimeout(1200);
    await page.screenshot({ path: path.join(outDir, "mockup-1440.png"), fullPage: true });

    // Composite: side by side + a difference heat map, drawn in-page from
    // data URLs (file:// images do not load in an about:blank page).
    const { readFile } = await import("node:fs/promises");
    const toDataUrl = async (p2) => {
      const buf = await readFile(p2);
      return `data:image/png;base64,${buf.toString("base64")}`;
    };
    const urlA = await toDataUrl(path.join(outDir, "home-1440.png"));
    const urlB = await toDataUrl(path.join(outDir, "mockup-1440.png"));
    const composite = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await composite.setContent("<canvas id='c'></canvas>");
    const stats = await composite.evaluate(
      async ([a, b]) => {
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
        const h = Math.min(imgA.height, imgB.height);
        const canvas = document.getElementById("c");
        canvas.width = w;
        canvas.height = h * 3;
        const ctx = canvas.getContext("2d");
        ctx.fillStyle = "#ffffff";
        ctx.fillRect(0, 0, canvas.width, canvas.height);
        ctx.drawImage(imgA, 0, 0);
        ctx.drawImage(imgB, 0, h);
        // diff: orange where pixels differ noticeably, light gray elsewhere
        const da = ctx.getImageData(0, 0, w, h);
        const db = ctx.getImageData(0, 0, w, h);
        const diff = ctx.createImageData(w, h);
        let changed = 0;
        for (let i = 0; i < da.data.length; i += 4) {
          const d =
            Math.abs(da.data[i] - db.data[i]) +
            Math.abs(da.data[i + 1] - db.data[i + 1]) +
            Math.abs(da.data[i + 2] - db.data[i + 2]);
          if (d > 24) {
            changed += 1;
            diff.data[i] = 227;
            diff.data[i + 1] = 89;
            diff.data[i + 2] = 12;
            diff.data[i + 3] = 255;
          } else {
            diff.data[i] = 235;
            diff.data[i + 1] = 235;
            diff.data[i + 2] = 235;
            diff.data[i + 3] = 255;
          }
        }
        ctx.putImageData(diff, 0, h * 2);
        return { changed, total: w * h, heightA: imgA.height, heightB: imgB.height };
      },
      [urlA, urlB],
    );
    await composite.locator("#c").screenshot({ path: path.join(outDir, "compare.png") });
    await composite.close();
    console.log("compare stats:", JSON.stringify(stats));

    // Mobile screenshots: / and a job page at 390 px.
    // The session for the mobile context and the API setup below.
    const login = await fetch(`${baseUrl}/api/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: "client@example.com", password: "client-password-123" }),
    });
    const cookie = /sl_session=[^;]+/.exec(login.headers.get("set-cookie") ?? "")?.[0];
    const mobileContext = await browser.newContext({ viewport: { width: 390, height: 844 } });
    const sessionValue = /sl_session=([^;]+)/.exec(cookie)?.[1];
    if (sessionValue) {
      await mobileContext.addCookies([{ name: "sl_session", value: sessionValue, url: baseUrl }]);
    }
    const mobile = await mobileContext.newPage();
    await mobile.goto(`${baseUrl}/`);
    await mobile.waitForTimeout(800);
    await mobile.screenshot({ path: path.join(outDir, "home-390.png"), fullPage: true });

    // A finished job for the order page.
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
    const deadline = Date.now() + 120_000;
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
    await mobile.waitForTimeout(1000);
    await mobile.getByText("Пояснительная записка").first().waitFor();
    await mobile.waitForTimeout(500);
    await mobile.screenshot({ path: path.join(outDir, "job-390.png"), fullPage: true });
    await mobile.close();
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
