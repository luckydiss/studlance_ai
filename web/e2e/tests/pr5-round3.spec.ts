import { type Page, expect, test } from "@playwright/test";
import { login, waitForApiStatus } from "./helpers";

// Round-3 regressions: late results of an old session instance must be
// ignored before any side effect, and uploaded file names must preserve
// their exact bytes (checked through the real worker input download).
//
// Mocks in this file are narrowly documented: a delayed HTTP delivery of a
// mutation response (the body/answer of the old request) and a login-form
// session fixture. Everything else runs against the real server, worker and
// fake agents.

/** Client-side navigation without a page reload (keeps SPA caches). */
async function spaNavigate(page: Page, to: string) {
  await page.evaluate((url) => {
    window.history.pushState({}, "", url);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, to);
}

/**
 * Installs a MutationObserver that records any node whose text contains the
 * marker, from installation until read(). Catches a stale title even if it
 * appears briefly and is replaced by a refetch afterwards.
 */
async function watchText(page: Page, marker: string) {
  await page.evaluate((text) => {
    const seen: string[] = [];
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (node instanceof Text && node.textContent?.includes(text)) {
            seen.push(node.textContent);
          } else if (node instanceof HTMLElement && node.innerText?.includes(text)) {
            seen.push(node.innerText);
          }
        }
        if (record.target instanceof Text && record.target.textContent?.includes(text)) {
          seen.push(record.target.textContent);
        }
      }
    });
    observer.observe(document.body, {
      childList: true,
      subtree: true,
      characterData: true,
    });
    (window as unknown as { __watchSeen: string[]; __watchText: string }).__watchSeen = seen;
    (window as unknown as { __watchText: string }).__watchText = text;
  }, marker);
}

async function readWatch(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { __watchSeen: string[] }).__watchSeen ?? []);
}

test("старый 200 после повторного входа A не появляется ни в одном DOM-обновлении", async ({
  page,
}) => {
  await login(page);
  // A needs_input order: the answer mutation is the fenced request.
  const resp = await page.request.post("/api/client/jobs", {
    data: { prompt: "Сделай практическую работу #ask" },
  });
  const job = (await resp.json()) as { id: string };
  await page.request.put(
    `/api/client/jobs/${job.id}/input?path=${encodeURIComponent("задание.pdf")}`,
    {
      data: "исходники",
    },
  );
  await page.request.post(`/api/client/jobs/${job.id}/submit`);
  await waitForApiStatus(page, job.id, "needs_input");

  await page.goto(`/orders/${job.id}`);
  await page.getByRole("textbox", { name: "Ваш ответ" }).fill("Первый ответ пользователя A");
  const answerButton = page.getByLabel("Нужно уточнение").getByRole("button", { name: "Ответить" });
  await expect(answerButton).toBeEnabled();

  // The answer POST is delayed and will deliver an OLD response body (a
  // document mock: the stale JobDetail with a marker title). Release is
  // controlled from the test.
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const staleBody = JSON.stringify({
    id: job.id,
    title: "УСТАРЕВШИЙ-ЗАГОЛОВОК-СТАРОЙ-СЕССИИ",
    prompt: "старый",
    client_status: "queued",
    status_text: "Заказ принят",
    current_version: 0,
    created_at: "2026-10-07T00:00:00Z",
    updated_at: "2026-10-07T00:00:00Z",
    question: null,
    status_steps: [],
    input_files: [],
    versions: [],
    planned_documents: [],
    revisions: [],
    can_cancel: false,
    can_revise: false,
    can_answer: false,
  });
  await page.route(/\/api\/client\/jobs\/[^/]+\/answer$/, async (route) => {
    await gate;
    await route.fulfill({ status: 200, contentType: "application/json", body: staleBody });
  });

  // A sends the answer; while the POST is in flight the session ends
  // server-side (no local logout) and A logs in again in the same tab.
  await answerButton.click();
  await page.request.post("/api/auth/logout");
  await spaNavigate(page, "/login");
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();

  // The fresh session loads the real, current job data.
  await spaNavigate(page, `/orders/${job.id}`);
  await expect(page.getByText("Нужно уточнение").first()).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("УСТАРЕВШИЙ-ЗАГОЛОВОК-СТАРОЙ-СЕССИИ")).toHaveCount(0);

  // Watch every DOM update from BEFORE the stale response is delivered.
  await watchText(page, "УСТАРЕВШИЙ-ЗАГОЛОВОК-СТАРОЙ-СЕССИИ");
  const delivered = page.waitForResponse((r) => r.url().includes("/answer"));
  release();
  // The old 200 is actually delivered to the still-pending mutation.
  await delivered;
  const seen = await readWatch(page);
  expect(seen).toEqual([]);
  await expect(page.getByText("УСТАРЕВШИЙ-ЗАГОЛОВОК-СТАРОЙ-СЕССИИ")).toHaveCount(0);
  // The current answer form is untouched by the old response.
  await expect(page.getByRole("textbox", { name: "Ваш ответ" })).toHaveValue("");
  // The real flow still works: A answers again and the job completes — the
  // delivery mock is removed so the request hits the real server.
  await page.unroute(/\/api\/client\/jobs\/[^/]+\/answer$/);
  await page.getByRole("textbox", { name: "Ваш ответ" }).fill("Повторный ответ пользователя A");
  await answerButton.click();
  await waitForApiStatus(page, job.id, "done");
});

test("старый 401 после входа B не открывает логин, не меняет маршрут и не удаляет сессию B", async ({
  page,
}) => {
  await login(page);
  const resp = await page.request.post("/api/client/jobs", {
    data: { prompt: "Сделай практическую работу #ask" },
  });
  const job = (await resp.json()) as { id: string };
  await page.request.put(
    `/api/client/jobs/${job.id}/input?path=${encodeURIComponent("задание.pdf")}`,
    {
      data: "исходники",
    },
  );
  await page.request.post(`/api/client/jobs/${job.id}/submit`);
  await waitForApiStatus(page, job.id, "needs_input");

  await page.goto(`/orders/${job.id}`);
  await page.getByRole("textbox", { name: "Ваш ответ" }).fill("Ответ пользователя A");
  const answerButton = page.getByLabel("Нужно уточнение").getByRole("button", { name: "Ответить" });

  // The old POST is delayed and will resolve with 401 after B's session is
  // already active. Release is controlled from the test.
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(/\/api\/client\/jobs\/[^/]+\/answer$/, async (route) => {
    await gate;
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      body: '{"error":{"code":"unauthorized","message":"Требуется вход"}}',
    });
  });

  await answerButton.click();
  await page.request.post("/api/auth/logout");
  await spaNavigate(page, "/login");
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  // B logs in in the same tab and stays on the home page.
  await page.getByLabel("Почта").fill("stranger@example.com");
  await page.getByLabel("Пароль").fill("stranger-password-3");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();
  const bAvatar = page.getByRole("button", { name: /Меню пользователя Другой Клиент/ });
  await expect(bAvatar).toBeVisible();

  // The old 401 is delivered now; it must not expire B's session, redirect
  // to the login form or drop B's user state.
  await watchText(page, "Вход в studlance");
  const delivered = page.waitForResponse((r) => r.url().includes("/answer"));
  release();
  await delivered;
  expect(await readWatch(page)).toEqual([]);
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();
  await expect(bAvatar).toBeVisible();
  // B's own data path still works after the stale error arrived.
  await spaNavigate(page, "/orders");
  await expect(page.getByRole("heading", { name: "Мои заказы" })).toBeVisible();
  await expect(page.getByText("Заказов пока нет")).toBeVisible({ timeout: 30_000 });
});

test("точные байты двух имён с пробелами и точная замена (через API воркера)", async ({ page }) => {
  test.skip(!process.env.E2E_WORKER_TOKEN || !process.env.E2E_ADMIN_COOKIE, "harness secrets");
  await login(page);
  const spacedBytes = Buffer.from("байты с пробелами в имени — точная проверка");
  const plainV1 = Buffer.from("другие байты — первая версия");
  const plainV2 = Buffer.from("третьи байты, длиннее прежних — точная замена");
  const folderBytes = Buffer.from("содержимое файла в папке с пробелами");

  await page.goto("/");
  await page.locator("#order-files").setInputFiles([
    { name: "  report.txt", mimeType: "text/plain", buffer: spacedBytes },
    { name: "report.txt", mimeType: "text/plain", buffer: plainV1 },
  ]);
  await page
    .locator("#order-files")
    .setInputFiles([{ name: "report.txt", mimeType: "text/plain", buffer: plainV2 }]);
  const { mkdir, writeFile } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const nodePath = await import("node:path");
  const dir = nodePath.join(tmpdir(), `studlance-e2e-bytes-${Date.now()}`);
  const folder = nodePath.join(dir, "Папка с пробелами");
  await mkdir(folder, { recursive: true });
  await writeFile(nodePath.join(folder, "файл с пробелом.txt"), folderBytes);
  await page.locator("#order-folder").setInputFiles(dir);
  // #slow keeps the agent busy for ~5 s: a comfortable window while the
  // job is running — worker input downloads are lease-checked.
  await page.getByRole("textbox", { name: "Запрос" }).fill("Сделай практическую работу #slow");
  await page.getByRole("button", { name: "Оформить заказ" }).click();
  await page.waitForURL(/\/orders\/[^/]+$/);
  const jobId = /\/orders\/([^/?#]+)/.exec(page.url())?.[1] ?? "";

  // Download the uploaded input through the real worker API (the actual
  // consumer of the files) and compare the exact bytes — while the job is
  // running, with the lease epoch from the admin view.
  // Plain Node fetch for the byte checks: the API request context of the
  // page rewrites the encoded path segment, the real consumer (the worker)
  // uses the URL exactly as constructed here.
  const base = process.env.E2E_BASE_URL ?? "";
  const adminHeaders = { Cookie: process.env.E2E_ADMIN_COOKIE ?? "" };
  const workerHeaders = { Authorization: `Bearer ${process.env.E2E_WORKER_TOKEN ?? ""}` };
  let epoch = 0;
  const deadline = Date.now() + 60_000;
  for (;;) {
    const admin = await fetch(`${base}/api/admin/jobs/${jobId}`, { headers: adminHeaders });
    expect(admin.status).toBe(200);
    const detail = (await admin.json()) as { status: string; lease_epoch: number };
    if (detail.status === "running") {
      epoch = detail.lease_epoch;
      break;
    }
    if (Date.now() > deadline) {
      throw new Error(`job did not start running: ${detail.status}`);
    }
    await page.waitForTimeout(150);
  }
  // The worker input endpoint expects the epoch as a query parameter and
  // the path as a single percent-encoded segment.
  const workerGetEpoch = async (path: string) => {
    const response = await fetch(
      `${base}/api/worker/jobs/${jobId}/input/${encodeURIComponent(path)}?epoch=${epoch}`,
      { headers: workerHeaders },
    );
    expect(response.status).toBe(200);
    return Buffer.from(await response.arrayBuffer());
  };
  expect((await workerGetEpoch("  report.txt")).equals(spacedBytes)).toBe(true);
  expect((await workerGetEpoch("report.txt")).equals(plainV2)).toBe(true);
  // The directory picker keeps the root folder name in the relative path.
  const folderPath = `${nodePath.basename(dir)}/Папка с пробелами/файл с пробелом.txt`;
  expect((await workerGetEpoch(folderPath)).equals(folderBytes)).toBe(true);
  // The replaced entry is byte-exact V2, not V1.
  expect((await workerGetEpoch("report.txt")).equals(plainV1)).toBe(false);
  await waitForApiStatus(page, jobId, "done");
});
