import { mkdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { type Page, expect, test } from "@playwright/test";
import { login, waitForApiStatus } from "./helpers";

// Review-round regressions for PR 5:
// 1. session switch must not leak the previous user's cache, a late response
//    of the old session must not resurface its data (P1);
// 2. 401 with a fresh cached /me must open the login form with next (P2);
// 3. file names with spaces survive byte-for-byte (P2);
// 4. carousel: hover/focus pause and the full 5×4 s cycle.
// The touch drawing and keyboard checks live in the 390 px test
// (pr5.spec.ts).

/** Client-side navigation without a page reload (keeps SPA caches). */
async function spaNavigate(page: Page, to: string) {
  await page.evaluate((url) => {
    window.history.pushState({}, "", url);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, to);
}

test("P1: смена пользователя не показывает чужой кеш, поздний ответ не возвращает данные", async ({
  page,
}) => {
  // A opens their own order; the title is cached in the SPA.
  await login(page);
  const resp = await page.request.post("/api/client/jobs", {
    data: { prompt: "Приватный заказ пользователя A для проверки смены сессии" },
  });
  const job = (await resp.json()) as { id: string };
  await page.request.put(
    `/api/client/jobs/${job.id}/input?path=${encodeURIComponent("задание.pdf")}`,
    {
      data: "исходники",
    },
  );
  await page.request.post(`/api/client/jobs/${job.id}/submit`);
  await page.goto(`/orders/${job.id}`);
  await expect(page.getByRole("heading", { name: "Приватный заказ пользователя A" })).toBeVisible();
  const listJson = await (await page.request.get("/api/client/jobs")).text();

  // A late list response (mocked network delay, the body is A's real data):
  // it lands after the old session ended and must not resurface for user B.
  // Only the first GET is intercepted; everything else stays real.
  let lateDelivered = false;
  let lateHits = 0;
  await page.route(/\/api\/client\/jobs$/, async (route) => {
    if (route.request().method() !== "GET" || lateHits > 0) {
      await route.continue();
      return;
    }
    lateHits += 1;
    await page.waitForTimeout(4000);
    lateDelivered = true;
    await route.fulfill({ status: 200, contentType: "application/json", body: listJson });
  });

  // The session dies server-side without pressing «Выйти» (no local logout):
  // the cookie is gone, the SPA keeps its caches.
  await page.request.post("/api/auth/logout");

  // A's list query starts (its response will arrive late)…
  await spaNavigate(page, "/orders");
  await expect(page.getByText("Загружаем…")).toBeVisible();
  // …and the login form appears after a client-side navigation: the cached
  // (fresh) me is re-verified and comes back 401.
  await spaNavigate(page, "/login");
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });

  // B logs in in the same tab; the previous caches are dropped before the
  // new session renders anything.
  await page.getByLabel("Почта").fill("stranger@example.com");
  await page.getByLabel("Пароль").fill("stranger-password-3");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();

  // B walks to A's order URL inside the SPA: the title must not appear even
  // before the answer (the cache is bound to the user) and the page 404s.
  await spaNavigate(page, `/orders/${job.id}`);
  await expect(page.getByText("Приватный заказ пользователя A")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Заказ не найден" })).toBeVisible({
    timeout: 30_000,
  });
  // The late response of the old session has landed by now; it was written
  // under A's key and cannot resurface in B's session.
  await expect.poll(() => lateDelivered, { timeout: 20_000 }).toBe(true);
  await expect(page.getByText("Приватный заказ пользователя A")).toHaveCount(0);

  // B's own list is empty and shows no trace of A.
  await spaNavigate(page, "/orders");
  await expect(page.getByText("Заказов пока нет")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("Приватный заказ пользователя A")).toHaveCount(0);
});

test("P2: 401 при свежем me — форма входа с next и возвратом (страница заказа)", async ({
  page,
}) => {
  await login(page);
  const resp = await page.request.post("/api/client/jobs", {
    data: { prompt: "Заказ для проверки 401 со свежим me" },
  });
  const job = (await resp.json()) as { id: string };
  await page.request.put(
    `/api/client/jobs/${job.id}/input?path=${encodeURIComponent("задание.pdf")}`,
    {
      data: "исходники",
    },
  );
  await page.request.post(`/api/client/jobs/${job.id}/submit`);
  await waitForApiStatus(page, job.id, "done");

  // me is cached and fresh from the login above; the job GET now answers 401
  // (a documented auth-failure mock — everything else stays real).
  const unauthorized = () =>
    page.route(/\/api\/client\/jobs\/[^/]+$/, async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: '{"error":{"code":"unauthorized","message":"Требуется вход"}}',
      });
    });

  await unauthorized();
  await page.goto(`/orders/${job.id}`);
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  await expect(page).toHaveURL(
    new RegExp(`login\\?next=${encodeURIComponent(`/orders/${job.id}`)}`),
  );

  // A plain network error is not 401: the page keeps its retry UI instead.
  // Disable the HTTP cache: this is the second page fetch of the same URL
  // and a cached response would bypass the route interception.
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Network.setCacheDisabled", { cacheDisabled: true });
  await page.unroute(/\/api\/client\/jobs\/[^/]+$/);
  // Abort the job GET and the SSE stream alike: with a live stream the page
  // legitimately recovers from a REST failure via the snapshot, so the retry
  // UI only shows when both are gone.
  await page.route(/\/api\/client\/jobs\/[^/]+/, async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    await route.abort();
  });
  await page.goto(`/orders/${job.id}`);
  await expect(page.getByText("Не удалось загрузить заказ")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toHaveCount(0);
  await page.unroute(/\/api\/client\/jobs\/[^/]+/);
  await cdp.send("Network.setCacheDisabled", { cacheDisabled: false });

  // A real 401 again, then a real login returns to the original page.
  await unauthorized();
  await page.goto(`/orders/${job.id}`);
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  await page.unroute(/\/api\/client\/jobs\/[^/]+$/);
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  // The login returned to the original order page (the fake agent rewrites
  // the title after the draft, so assert the route and the kit instead).
  await expect(page).toHaveURL(new RegExp(`/orders/${job.id}`), { timeout: 30_000 });
  await expect(page.getByText("Пояснительная записка").first()).toBeVisible({ timeout: 30_000 });
});

test("P2: 401 списка заказов при свежем me — форма с next=/orders", async ({ page }) => {
  await login(page);
  // me is fresh (login above); SPA-navigate to the list without a reload.
  await spaNavigate(page, "/orders");
  await expect(page.getByRole("heading", { name: "Мои заказы" })).toBeVisible();

  await page.route(/\/api\/client\/jobs$/, async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      body: '{"error":{"code":"unauthorized","message":"Требуется вход"}}',
    });
  });
  await page.goto("/orders");
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  await expect(page).toHaveURL(/\/login\?next=%2Forders/);
  await page.unroute(/\/api\/client\/jobs$/);
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByRole("heading", { name: "Мои заказы" })).toBeVisible({ timeout: 30_000 });
});

test("P2: имена файлов с пробелами сохраняются, замена только по точному пути", async ({
  page,
}) => {
  await login(page);
  // Two names that differ only by spaces (Windows allows both), a folder
  // with spaces, and a file-only order (empty prompt).
  const dir = path.join(tmpdir(), `studlance-e2e-spaces-${Date.now()}`);
  const folder = path.join(dir, "Папка с пробелами");
  await mkdir(folder, { recursive: true });
  await writeFile(path.join(folder, "файл с пробелом.txt"), "содержимое файла в папке", "utf8");
  const plainV1 = Buffer.from("другие байты");
  const plainV2 = Buffer.from("третьи байты, длиннее прежних");

  await page.goto("/");
  await page.locator("#order-files").setInputFiles([
    {
      name: "  report.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("байты с пробелами в имени"),
    },
    { name: "report.txt", mimeType: "text/plain", buffer: plainV1 },
  ]);
  // Re-adding the exact same path replaces only that entry.
  await page
    .locator("#order-files")
    .setInputFiles([{ name: "report.txt", mimeType: "text/plain", buffer: plainV2 }]);
  await page.locator("#order-folder").setInputFiles(dir);
  await page.getByRole("button", { name: "Оформить заказ" }).click();
  await page.waitForURL(/\/orders\/[^/]+$/);
  const jobId = /\/orders\/([^/?#]+)/.exec(page.url())?.[1] ?? "";

  // The server sees both original paths with different sizes (different
  // bytes), the folder path with spaces, and the replaced file's new size.
  await waitForApiStatus(page, jobId, "done");
  const detail = await (await page.request.get(`/api/client/jobs/${jobId}`)).json();
  const files: { path: string; size: number }[] = detail.input_files;
  const paths = files.map((f) => f.path);
  expect(paths).toContain("  report.txt");
  expect(paths).toContain("report.txt");
  expect(paths.some((p) => p.includes("Папка с пробелами/файл с пробелом.txt"))).toBe(true);
  const spaced = files.find((f) => f.path === "  report.txt");
  const plain = files.find((f) => f.path === "report.txt");
  expect(spaced?.size).not.toBe(plain?.size);
  expect(plain?.size).toBe(plainV2.length);
  // Exactly one entry per path: no silent merge of the two names.
  expect(paths.filter((p) => p.trim() === "report.txt")).toHaveLength(2);
});

test("карусель: пауза hover и focus, полный цикл 5×4 с", async ({ page }) => {
  await login(page);
  const stage = () =>
    page.evaluate(() =>
      document.querySelector("[data-stage-active]")?.getAttribute("data-stage-active"),
    );

  // Auto-advance is running (normal motion): the stage changes on its own.
  await expect.poll(stage, { timeout: 8000 }).not.toBe("0");

  // Hover pauses: the stage holds for longer than one stage duration.
  await page.locator("[data-stage-active]").hover();
  const duringHover = await stage();
  await page.waitForTimeout(5500);
  expect(await stage()).toBe(duringHover);

  // Focus inside the carousel also pauses.
  await page.mouse.move(0, 0);
  await page.waitForTimeout(300);
  await page.getByRole("button", { name: "Этап 1: Загрузили исходники" }).focus();
  const duringFocus = await stage();
  await page.waitForTimeout(5500);
  expect(await stage()).toBe(duringFocus);

  // Full cycle: from the last stage the carousel wraps back to the first.
  await page.getByRole("button", { name: "Этап 5: Готовый комплект" }).click();
  await expect.poll(stage, { timeout: 2000 }).toBe("4");
  // The click leaves the mouse hovering the carousel (a pause of its own):
  // move it away and drop the focus so the auto-advance resumes.
  await page.mouse.move(0, 0);
  await page.getByRole("button", { name: "Этап 5: Готовый комплект" }).blur();
  await expect.poll(stage, { timeout: 10_000 }).toBe("0");
});
