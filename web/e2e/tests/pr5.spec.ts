import { mkdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { type Page, expect, test } from "@playwright/test";
import {
  createOrder,
  jobIdFromUrl,
  login,
  logout,
  waitForApiStatus,
  waitForStatus,
} from "./helpers";

// PR 5 acceptance scenarios (09-tasks.md / 07-web-client.md):
// the whole flow runs against the real Go server + worker + fake agents.

let uploadDir: string;

/**
 * The status window is fixed bottom-right; on `done` it stays expanded for
 * 10 s and can cover the revision panel. Collapse it like a user would.
 */
async function collapseStatusWindow(page: Page) {
  const win = page.getByLabel("Статус заказа");
  if ((await win.count()) === 0) {
    return;
  }
  const toggle = win.getByRole("button").first();
  if ((await toggle.getAttribute("aria-expanded")) === "true") {
    await toggle.click();
  }
}

/**
 * Scrolls the button to the viewport start and clicks it: the status window
 * is fixed bottom-right and can otherwise cover the target.
 */
async function clickAboveStatusWindow(page: Page, name: string) {
  const btn = page.getByRole("button", { name });
  await btn.evaluate((el) => el.scrollIntoView({ block: "start" }));
  await btn.click();
}

test.beforeAll(async () => {
  uploadDir = path.join(tmpdir(), `studlance-e2e-upload-${Date.now()}`);
  const nested = path.join(uploadDir, "Методичка кафедры", "Варианты");
  await mkdir(nested, { recursive: true });
  await writeFile(path.join(nested, "вариант 14.pdf"), "выдуманное задание, вариант 14", "utf8");
  await writeFile(path.join(nested, "титульный лист.txt"), "выдуманный титульный лист", "utf8");
  await writeFile(path.join(uploadDir, "задание.pdf"), "выдуманное задание без папки", "utf8");
});

test("вход: ошибка, успех, выход, возврат по next и 401", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("неверный-пароль");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByText("Неверная почта или пароль")).toBeVisible();

  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();

  await logout(page);

  // 401 -> /login with next; after login returns to /orders.
  await page.goto("/orders");
  await expect(page).toHaveURL(/\/login/);
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page).toHaveURL(/\/orders$/);
});

test("чужой заказ по прямой ссылке — 404 «Заказ не найден»", async ({ browser }) => {
  const owner = await browser.newContext();
  const page = await owner.newPage();
  await login(page);
  const url = await createOrder(page, "Заказ для проверки чужого доступа к карточке");
  const jobId = /\/orders\/([^/?#]+)/.exec(url)?.[1] ?? "";
  await owner.close();

  const stranger = await browser.newContext();
  const strangerPage = await stranger.newPage();
  await login(strangerPage, "stranger");
  await strangerPage.goto(`/orders/${jobId}`);
  await expect(strangerPage.getByText("Заказ не найден")).toBeVisible();
  await stranger.close();
});

test("новый заказ с файлами и вложенной папкой → готово v1, листы, скачивание", async ({
  page,
}) => {
  await login(page);
  await createOrder(page, "Сделай практическую работу: расчёт балки по данным из файла", {
    dir: uploadDir,
  });
  const jobId = jobIdFromUrl(page);

  // While uploading: no status window. After files land, submit happens
  // automatically and the status window appears.
  await waitForStatus(page, "Заказ принят");
  const body = await waitForApiStatus(page, jobId, "done");
  expect(body.current_version).toBeGreaterThanOrEqual(1);
  await waitForStatus(page, "Готово");

  // The kit viewer shows documents and pages.
  await expect(page.getByText("Пояснительная записка").first()).toBeVisible();

  // Keyboard arrows flip pages (the viewer listens on window keydown).
  await expect(page.getByText("лист 1 из")).toBeVisible();
  await page.keyboard.press("ArrowRight");
  await expect(page.getByText("лист 2 из")).toBeVisible({ timeout: 10_000 });

  // Download a document file and the bundle archive via the real endpoints.
  const [fileDownload] = await Promise.all([
    page.waitForEvent("download"),
    page.getByRole("link", { name: "Скачать", exact: true }).first().click(),
  ]);
  expect((await fileDownload.suggestedFilename()).length).toBeGreaterThan(0);

  const [bundleDownload] = await Promise.all([
    page.waitForEvent("download"),
    page.getByRole("link", { name: "Скачать всё" }).click(),
  ]);
  expect(await bundleDownload.suggestedFilename()).toBe("bundle.zip");
});

test("заказ только с файлами (пустой запрос) и заказ только текстом", async ({ page }) => {
  await login(page);

  // file-only: empty prompt is allowed, the button activates with a file.
  await page.goto("/");
  const fileInput = page.locator('input[type="file"]:not([webkitdirectory])');
  await fileInput.setInputFiles(path.join(uploadDir, "задание.pdf"));
  await page.getByRole("button", { name: "Оформить заказ" }).click();
  await page.waitForURL(/\/orders\/[^/]+$/);
  const fileOnlyId = jobIdFromUrl(page);
  const fileOnly = await waitForApiStatus(page, fileOnlyId, "done");
  expect(fileOnly.current_version).toBe(1);

  // text-only: a 20+ chars prompt without files.
  await page.goto("/");
  await page
    .getByRole("textbox", { name: "Запрос" })
    .fill("Текстовый заказ без вложений, данных достаточно в запросе");
  await page.getByRole("button", { name: "Оформить заказ" }).click();
  await page.waitForURL(/\/orders\/[^/]+$/);
  const textOnlyId = jobIdFromUrl(page);
  await waitForApiStatus(page, textOnlyId, "done");
});

test("uploading: окошко статуса скрыто, дозагрузка и отправка со страницы заказа", async ({
  page,
}) => {
  await login(page);
  // Prepare a draft through the API (empty prompt, no files yet).
  const resp = await page.request.post("/api/client/jobs", { data: { prompt: "" } });
  expect(resp.status()).toBe(201);
  const job = (await resp.json()) as { id: string };
  await page.goto(`/orders/${job.id}`);

  // Uploading: no status window on the page.
  await expect(page.getByLabel("Статус заказа")).toBeHidden();

  // Files upload through the dropzone/picker on the job page.
  await page.getByRole("button", { name: "Прикрепить файлы или папку" }).first().click();
  await page
    .locator('input[aria-label="Файлы"]')
    .first()
    .setInputFiles(path.join(uploadDir, "задание.pdf"));
  await expect(page.getByText("задание.pdf").first()).toBeVisible({ timeout: 30_000 });

  // Submit becomes possible and moves the order along.
  await page.getByRole("button", { name: "Отправить заказ" }).click();
  await waitForApiStatus(page, job.id, "done");
});

test("неудачная загрузка → повтор на том же заказе, без нового заказа", async ({ page }) => {
  await login(page);
  await page.goto("/");
  await page
    .getByRole("textbox", { name: "Запрос" })
    .fill("Заказ для проверки повторов загрузки файлов");

  // Fail every upload attempt for the first order: the app retries 3 times
  // per file, then shows the retry UI. A targeted failure mock — the rest of
  // the flow keeps hitting the real server.
  let failures = 0;
  await page.route(/\/api\/client\/jobs\/[^/]+\/input/, async (route) => {
    failures += 1;
    await route.fulfill({
      status: 500,
      contentType: "application/json",
      body: '{"error":{"code":"internal","message":"сбой"}}',
    });
  });
  await page
    .locator('input[type="file"]:not([webkitdirectory])')
    .setInputFiles(path.join(uploadDir, "задание.pdf"));
  await page.getByRole("button", { name: "Оформить заказ" }).click();
  await expect(page.getByRole("button", { name: "Повторить" })).toBeVisible({ timeout: 60_000 });
  await expect(
    page.getByText("Не удалось загрузить задание.pdf. Попробуйте ещё раз"),
  ).toBeVisible();
  expect(failures).toBeGreaterThanOrEqual(4); // 1 attempt + 3 retries

  // The failed draft exists exactly once.
  const jobs = await (await page.request.get("/api/client/jobs")).json();
  const drafts = jobs.jobs.filter(
    (j: { title: string }) => j.title === "Заказ для проверки повторов загрузки файлов",
  );
  expect(drafts).toHaveLength(1);
  const jobId = drafts[0].id;

  // Retry with the network fixed: same job, upload succeeds, submit works.
  await page.unroute(/\/api\/client\/jobs\/[^/]+\/input/);
  await page.getByRole("button", { name: "Повторить" }).click();
  await waitForApiStatus(page, jobId, ["queued", "accepted", "in_progress", "done"]);
});

test("#ask: вопрос → ответ клиента → готово", async ({ page }) => {
  await login(page);
  await createOrder(page, "Сделай практическую работу #ask");
  const jobId = jobIdFromUrl(page);
  await waitForStatus(page, "Нужно уточнение");
  const detail = await waitForApiStatus(page, jobId, "needs_input");
  expect(detail.question?.length ?? 0).toBeGreaterThan(0);

  await page
    .getByRole("textbox", { name: "Ваш ответ" })
    .fill("Данные в приложенном файле, вариант 14");
  await page.getByLabel("Нужно уточнение").getByRole("button", { name: "Ответить" }).click();
  await waitForApiStatus(page, jobId, "done");
});

test("#hang: отмена заказа клиентом", async ({ page }) => {
  await login(page);
  await createOrder(page, "Сделай практическую работу #hang");
  const jobId = jobIdFromUrl(page);

  await page.getByRole("button", { name: "Отменить заказ" }).click();
  await expect(page.getByText("Отменить заказ? Работа будет остановлена.")).toBeVisible();
  await page.getByRole("button", { name: "Отменить", exact: true }).click();
  await expect(page.getByText("Заказ отменён")).toBeVisible({ timeout: 60_000 });
  await waitForApiStatus(page, jobId, "canceled");
});

test("доработка: две рамки и вложение → версия 2, привязка замечаний", async ({ page }) => {
  await login(page);
  await createOrder(page, "Сделай практическую работу: расчёт балки");
  const jobId = jobIdFromUrl(page);
  await waitForApiStatus(page, jobId, "done");
  await waitForStatus(page, "Готово");

  // Enable the remark mode and draw two rectangles on the displayed sheet.
  await collapseStatusWindow(page);
  await clickAboveStatusWindow(page, "Нужна доработка");
  const sheet = page.locator('main img[alt*="лист"]').first();
  await sheet.waitFor({ state: "visible" });
  const box = await sheet.boundingBox();
  if (!box) {
    throw new Error("sheet has no bounding box");
  }

  const drawRemark = async (fx: number, fy: number) => {
    // Bring the sheet to the viewport top: the sheet is taller than the
    // viewport and pointer events outside it are not delivered at all.
    await sheet.evaluate((el) => el.scrollIntoView({ block: "start" }));
    const b = await sheet.boundingBox();
    if (!b) {
      throw new Error("sheet has no bounding box");
    }
    await page.mouse.move(b.x + b.width * fx, b.y + b.height * fy);
    await page.mouse.down();
    await page.mouse.move(b.x + b.width * (fx + 0.3), b.y + b.height * (fy + 0.06), {
      steps: 8,
    });
    await page.mouse.up();
    const popover = page.getByRole("textbox", { name: "Что поправить?" });
    await popover.waitFor({ state: "visible" });
    await popover.fill("Замечание про это место на листе");
    await page.getByRole("button", { name: "Добавить" }).click();
  };
  await drawRemark(0.2, 0.3);
  await drawRemark(0.5, 0.6);

  // Attach a file and send the revision.
  await page.getByRole("button", { name: "Приложить файлы" }).click();
  await page
    .locator('input[aria-label="Файлы"]')
    .last()
    .setInputFiles(path.join(uploadDir, "задание.pdf"));
  await page.getByRole("button", { name: "Отправить на доработку" }).click();

  const revising = await waitForApiStatus(page, jobId, "revising");
  expect(revising.revisions.at(-1)?.remarks).toHaveLength(2);
  await waitForStatus(page, "Дорабатываем");
  const done = await waitForApiStatus(page, jobId, "done");
  expect(done.current_version).toBe(2);

  // Remarks of the v1 revision stay bound to v1 pages, v2 shows none.
  await page.getByRole("button", { name: "Версия 1", exact: true }).click();
  await expect(page.getByText("Это версия 1. Последняя — версия 2")).toBeVisible();
  await expect
    .poll(async () => page.locator("[data-remark-frame]").count(), { timeout: 10_000 })
    .toBeGreaterThan(0);
  await page.getByRole("button", { name: /Версия 2/ }).click();
  await expect(page.locator("[data-remark-frame]")).toHaveCount(0, { timeout: 10_000 });
});

test("SSE: обрыв потока → переподключение со snapshot", async ({ page }) => {
  await login(page);
  const resp = await page.request.post("/api/client/jobs", {
    data: { prompt: "Заказ для проверки переподключения потока событий" },
  });
  const job = (await resp.json()) as { id: string };
  await page.request.put(
    `/api/client/jobs/${job.id}/input?path=${encodeURIComponent("задание.pdf")}`,
    {
      data: "исходники",
    },
  );
  await page.request.post(`/api/client/jobs/${job.id}/submit`);

  // Abort the first stream request: EventSource reconnects with backoff and
  // a fresh snapshot restores the state.
  let streams = 0;
  await page.route(/\/api\/client\/jobs\/[^/]+\/stream/, async (route) => {
    streams += 1;
    if (streams === 1) {
      await route.abort();
      return;
    }
    await route.continue();
  });
  await page.goto(`/orders/${job.id}`);
  await expect(
    page.getByText("Заказ для проверки переподключения потока событий").first(),
  ).toBeVisible({
    timeout: 30_000,
  });
  await expect.poll(() => streams, { timeout: 30_000 }).toBeGreaterThanOrEqual(2);
  await page.unroute(/\/api\/client\/jobs\/[^/]+\/stream/);
  await waitForApiStatus(page, job.id, "done");
});

test("390 px: нет горизонтального переполнения, режим замечаний работает", async ({
  page,
  browser,
}) => {
  await login(page);
  const resp = await page.request.post("/api/client/jobs", {
    data: { prompt: "Заказ для мобильной проверки интерфейса" },
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

  const mobile = await browser.newContext({
    viewport: { width: 390, height: 844 },
    hasTouch: true,
  });
  const m = await mobile.newPage();
  await login(m);
  await m.goto("/");
  const homeOverflow = await m.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(homeOverflow).toBeLessThanOrEqual(1);

  await m.goto(`/orders/${job.id}`);
  await m.getByText("Пояснительная записка").first().waitFor();
  const jobOverflow = await m.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(jobOverflow).toBeLessThanOrEqual(1);

  // Keyboard controls: focus lands on real controls and Enter activates
  // the next-page button.
  await collapseStatusWindow(m);
  await m.getByRole("button", { name: "Следующая страница" }).focus();
  await m.keyboard.press("Enter");
  await m.getByText("лист 2 из").waitFor();

  // Touch drawing (a real finger, not a renamed mouse) on a scaled-down
  // sheet still produces valid fractions.
  await clickAboveStatusWindow(m, "Нужна доработка");
  const sheet = m.locator('main img[alt*="лист"]').first();
  await sheet.waitFor({ state: "visible" });
  await sheet.evaluate((el) => el.scrollIntoView({ block: "start" }));
  const box = await sheet.boundingBox();
  if (!box) {
    throw new Error("mobile sheet has no bounding box");
  }
  const cdp = await m.context().newCDPSession(m);
  const touch = (type: "touchStart" | "touchMove" | "touchEnd", x?: number, y?: number) =>
    cdp.send("Input.dispatchTouchEvent", {
      type,
      touchPoints: x === undefined || y === undefined ? [] : [{ x, y }],
    });
  await touch("touchStart", box.x + box.width * 0.3, box.y + box.height * 0.3);
  for (let step = 1; step <= 6; step += 1) {
    await touch(
      "touchMove",
      box.x + box.width * (0.3 + 0.05 * step),
      box.y + box.height * (0.3 + 0.01 * step),
    );
  }
  await touch("touchEnd");
  await m.getByRole("textbox", { name: "Что поправить?" }).fill("Мобильное замечание");
  await m.getByRole("button", { name: "Добавить" }).click();
  await m.getByRole("button", { name: "Отправить на доработку" }).click();
  await waitForApiStatus(m, job.id, "revising");
  await mobile.close();
});

test("reduced-motion: карусель без автопрокрутки, клик по полоске работает", async ({
  browser,
}) => {
  const ctx = await browser.newContext({ reducedMotion: "reduce" });
  const page = await ctx.newPage();
  await login(page);
  await page.waitForTimeout(5500); // longer than one 4s stage
  const activeBefore = await page.evaluate(() => {
    const el = document.querySelector("[data-stage-active]");
    return el?.getAttribute("data-stage-active") ?? "";
  });
  // No auto-advance: still on the first stage.
  expect(activeBefore === "0" || activeBefore === "").toBe(true);

  await page.getByRole("button", { name: /Этап 3/ }).click();
  await expect
    .poll(async () =>
      page.evaluate(() =>
        document.querySelector("[data-stage-active]")?.getAttribute("data-stage-active"),
      ),
    )
    .toBe("2");
  await ctx.close();
});

test("demo-картинки отсутствуют: белые листы, вёрстка не ломается", async ({ page }) => {
  await login(page);
  // Demo files are absent in the harness. The home page must render with
  // white sheets of the same size and no broken-image glyphs: every img is
  // either source-less (the fallback) or loaded.
  await expect(page.getByText("Любые работы — готовым комплектом")).toBeVisible();
  await page.waitForTimeout(1000);
  const imgs = await page.evaluate(() =>
    Array.from(document.querySelectorAll("img")).map((i) => ({
      src: i.getAttribute("src") ?? "",
      ok: i.naturalWidth > 0,
    })),
  );
  for (const img of imgs) {
    expect(img.src === "" || img.ok).toBe(true);
  }
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
});

test("SPA deep link /orders/:id после перезагрузки", async ({ page }) => {
  await login(page);
  const resp = await page.request.post("/api/client/jobs", {
    data: { prompt: "Заказ для проверки deep link перезагрузкой" },
  });
  const job = (await resp.json()) as { id: string };
  await page.goto(`/orders/${job.id}`);
  await page.reload();
  await expect(page.getByText("Заказ для проверки deep link перезагрузкой").first()).toBeVisible();
});
