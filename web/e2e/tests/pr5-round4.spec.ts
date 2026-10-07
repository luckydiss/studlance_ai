import { expect, test } from "@playwright/test";
import { login } from "./helpers";

// Round-4 regressions: a 401 of the CURRENT session's manual PUT /input ends
// the local session (expireSession → login form with next pointing at the
// created order) and does not re-create the order; a late PUT 401 of an old
// session is ignored entirely. Fixtures: a documented 401 mock on the PUT
// endpoint; POST /jobs, login and everything else stay real.

const UNAUTHORIZED = JSON.stringify({
  error: { code: "unauthorized", message: "Требуется вход" },
});

test("401 PUT /input при оформлении: форма входа, next на созданный заказ, без второго POST", async ({
  page,
}) => {
  await login(page);

  let creates = 0;
  page.on("request", (req) => {
    if (req.method() === "POST" && req.url().endsWith("/api/client/jobs")) {
      creates += 1;
    }
  });
  // The PUT is rejected with 401 while the session is otherwise live.
  await page.route(/\/api\/client\/jobs\/[^/]+\/input/, async (route) => {
    if (route.request().method() !== "PUT") {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      body: UNAUTHORIZED,
    });
  });

  await page.goto("/");
  await page.getByRole("textbox", { name: "Запрос" }).fill("Заказ для проверки 401 загрузки");
  await page.locator("#order-files").setInputFiles([
    {
      name: "задание.pdf",
      mimeType: "application/octet-stream",
      buffer: Buffer.from("исходники"),
    },
  ]);
  await page.getByRole("button", { name: "Оформить заказ" }).click();

  // The local session is over: the login form stays, next points at the
  // already-created order, and no second POST /jobs was made.
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  expect(creates).toBe(1);
  const nextMatch = /next=(%2Forders%2F[^&]+)/.exec(page.url());
  if (!nextMatch) {
    throw new Error(`no order next in ${page.url()}`);
  }
  const jobId = decodeURIComponent(nextMatch[1]).replace("/orders/", "");
  expect(jobId.length).toBeGreaterThan(0);

  // After the login the same order opens for the top-up, without a new POST.
  await page.unroute(/\/api\/client\/jobs\/[^/]+\/input/);
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page).toHaveURL(new RegExp(`/orders/${jobId}`), { timeout: 30_000 });
  await expect(page.getByRole("button", { name: "Отправить заказ" })).toBeVisible();
  expect(creates).toBe(1);
});

test("401 PUT /input при дозагрузке в существующем заказе", async ({ page }) => {
  await login(page);
  const resp = await page.request.post("/api/client/jobs", { data: { prompt: "" } });
  const job = (await resp.json()) as { id: string };

  await page.route(/\/api\/client\/jobs\/[^/]+\/input/, async (route) => {
    if (route.request().method() !== "PUT") {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      body: UNAUTHORIZED,
    });
  });
  await page.goto(`/orders/${job.id}`);
  await page.getByRole("button", { name: "Прикрепить файлы или папку" }).first().click();
  await page
    .locator('input[aria-label="Файлы"]')
    .first()
    .setInputFiles([
      { name: "задание.pdf", mimeType: "application/octet-stream", buffer: Buffer.from("x") },
    ]);
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  await expect(page).toHaveURL(new RegExp(`login\\?next=%2Forders%2F${job.id}`));

  await page.unroute(/\/api\/client\/jobs\/[^/]+\/input/);
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page).toHaveURL(new RegExp(`/orders/${job.id}`), { timeout: 30_000 });
});

test("запоздалый PUT 401 старой сессии после входа B игнорируется до expire/navigate/toast", async ({
  page,
}) => {
  await login(page);
  const resp = await page.request.post("/api/client/jobs", { data: { prompt: "" } });
  const job = (await resp.json()) as { id: string };

  // Delayed delivery of the old PUT (starts under A, delivered after B's login).
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const started = new Promise<void>((resolve) => {
    page.route(/\/api\/client\/jobs\/[^/]+\/input/, async (route) => {
      if (route.request().method() !== "PUT") {
        await route.continue();
        return;
      }
      resolve();
      await gate;
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: UNAUTHORIZED,
      });
    });
  });

  await page.goto(`/orders/${job.id}`);
  await page.getByRole("button", { name: "Прикрепить файлы или папку" }).first().click();
  const pickPromise = page
    .locator('input[aria-label="Файлы"]')
    .first()
    .setInputFiles([
      { name: "задание.pdf", mimeType: "application/octet-stream", buffer: Buffer.from("x") },
    ]);
  await started; // the PUT has started before the session ends
  await page.request.post("/api/auth/logout");

  // A's session is gone; B logs in in the same tab (client-side navigation:
  // a document reload would cancel the in-flight PUT) and lands on /.
  await page.evaluate(() => {
    window.history.pushState({}, "", "/login");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  await page.getByLabel("Почта").fill("stranger@example.com");
  await page.getByLabel("Пароль").fill("stranger-password-3");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();
  await pickPromise;

  // The old PUT is delivered now; B stays on /, no login form appears.
  release();
  await page.waitForResponse((r) => r.url().includes("/input"));
  await page.waitForTimeout(300);
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toHaveCount(0);
});
