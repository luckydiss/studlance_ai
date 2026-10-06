// Lighthouse accessibility audit for PR 5 (09-tasks.md: >= 90 on / and
// /orders/:id with an authenticated test order). Boots the same harness as
// the Playwright run, logs in through the API and passes the session cookie
// via --extra-headers. Reports land in web/e2e/.artifacts/lighthouse.

import { spawn } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { startHarness } from "./harness.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.join(here, ".artifacts", "lighthouse");

function run(cmd, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { stdio: "inherit" });
    child.on("error", reject);
    child.on("close", (code) =>
      code === 0 ? resolve() : reject(new Error(`${cmd} exited ${code}`)),
    );
  });
}

/** Runs the locally installed lighthouse CLI with safe (non-shell) args. */
function runLighthouse(url, outPath, format, cookie, chromePath) {
  const cli = path.join(here, "node_modules", "lighthouse", "cli", "index.js");
  return run(process.execPath, [
    cli,
    url,
    "--only-categories=accessibility",
    `--output-path=${outPath}`,
    `--output=${format}`,
    `--chrome-path=${chromePath}`,
    `--extra-headers=${JSON.stringify({ Cookie: cookie })}`,
    "--quiet",
  ]);
}

async function main() {
  await mkdir(outDir, { recursive: true });
  const harness = await startHarness();
  const baseUrl = harness.baseUrl;
  try {
    // Log in and prepare a finished test order.
    const loginResp = await fetch(`${baseUrl}/api/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: "client@example.com", password: "client-password-123" }),
    });
    if (!loginResp.ok) {
      throw new Error(`login failed: ${loginResp.status}`);
    }
    const setCookie = loginResp.headers.get("set-cookie") ?? "";
    const cookie = /sl_session=([^;]+)/.exec(setCookie)?.[0];
    if (!cookie) {
      throw new Error("no sl_session cookie");
    }
    const api = async (method, url, body) => {
      const resp = await fetch(`${baseUrl}${url}`, {
        method,
        headers: { Cookie: cookie, ...(body ? { "Content-Type": "application/json" } : {}) },
        body: body ? JSON.stringify(body) : undefined,
      });
      return {
        status: resp.status,
        json: resp.status === 204 ? null : await resp.json().catch(() => null),
      };
    };
    const created = await api("POST", "/api/client/jobs", {
      prompt: "Заказ для аудита доступности страницы заказа",
    });
    const jobId = created.json.id;
    await fetch(
      `${baseUrl}/api/client/jobs/${jobId}/input?path=${encodeURIComponent("задание.pdf")}`,
      {
        method: "PUT",
        headers: { Cookie: cookie, "Content-Type": "application/octet-stream" },
        body: "исходные данные",
      },
    );
    await api("POST", `/api/client/jobs/${jobId}/submit`);
    const deadline = Date.now() + 180_000;
    let job = null;
    for (;;) {
      const detail = await api("GET", `/api/client/jobs/${jobId}`);
      job = detail.json;
      if (job.client_status === "done") {
        break;
      }
      if (Date.now() > deadline) {
        throw new Error(`job did not finish for lighthouse: ${job.client_status}`);
      }
      await new Promise((r) => setTimeout(r, 500));
    }

    const chromePath = chromium.executablePath();
    const targets = [
      { name: "home", url: `${baseUrl}/` },
      { name: "job", url: `${baseUrl}/orders/${jobId}` },
    ];
    const results = {};
    for (const t of targets) {
      const htmlPath = path.join(outDir, `${t.name}.report.html`);
      const jsonPath = path.join(outDir, `${t.name}.report.json`);
      // HTML for the report artifact, JSON to read the score.
      await runLighthouse(t.url, htmlPath, "html", cookie, chromePath);
      await runLighthouse(t.url, jsonPath, "json", cookie, chromePath);
      const report = JSON.parse(
        await import("node:fs/promises").then((fs) => fs.readFile(jsonPath, "utf8")),
      );
      results[t.name] = report.categories.accessibility.score * 100;
    }
    const summary = {
      base: baseUrl,
      job: jobId,
      scores: results,
      pass: Object.values(results).every((s) => s >= 90),
    };
    await writeFile(path.join(outDir, "summary.json"), JSON.stringify(summary, null, 2), "utf8");
    console.log(JSON.stringify(summary, null, 2));
    if (!summary.pass) {
      process.exitCode = 1;
    }
  } finally {
    await harness.stop();
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
