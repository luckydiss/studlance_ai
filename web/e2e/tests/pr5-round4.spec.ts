import { type Page, expect, test } from "@playwright/test";
import { login } from "./helpers";

// Round-4 regressions: a 401 of the CURRENT session's manual PUT /input ends
// the local session and the auth gate opens the login form with a return path
// recorded by the expiry (the created order) — not the original `/` — and no
// second order is created; a late PUT 401 of an old session is ignored.
// Fixtures: a documented 401 mock on the PUT endpoint; POST /jobs, login and
// everything else stay real.

const UNAUTHORIZED = JSON.stringify({
  error: { code: "unauthorized", message: "Требуется вход" },
});

/**
 * Records every SPA history change (pushState/replaceState) so a test can
 * assert on the FULL set of navigations the app made, not only the final URL.
 * This is what proves the ordering guarantee: no redirect in the sequence may
 * carry the raw home path as `next` when the order return path is known.
 */
async function recordNavigations(page: Page) {
  await page.addInitScript(() => {
    const log: string[] = [];
    (window as unknown as { __navLog: string[] }).__navLog = log;
    const record = (url: string | URL | null | undefined) => {
      log.push(typeof url === "string" ? url : String(url ?? ""));
    };
    const push = History.prototype.pushState;
    const replace = History.prototype.replaceState;
    History.prototype.pushState = function (
      this: History,
      ...args: Parameters<History["pushState"]>
    ) {
      record(args[2]);
      return push.apply(this, args);
    };
    History.prototype.replaceState = function (
      this: History,
      ...args: Parameters<History["replaceState"]>
    ) {
      record(args[2]);
      return replace.apply(this, args);
    };
  });
}

async function readNavigations(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { __navLog: string[] }).__navLog ?? []);
}

test("401 PUT /input при оформлении: единственный POST, конечный next на созданный заказ", async ({
  page,
}) => {
  await recordNavigations(page);
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
  // Register the wait BEFORE the request exists, then confirm the 401 PUT is
  // actually delivered and fully received before asserting anything.
  const putResponse = page.waitForResponse(
    (r) => r.request().method() === "PUT" && /\/input\b/.test(r.url()),
  );
  await page.getByRole("button", { name: "Оформить заказ" }).click();
  const put = await putResponse;
  expect(put.status()).toBe(401);
  await put.finished();

  // The local session is over: the login form stays and no second POST was
  // made. `next` is read from the FINAL URL after the gate redirect.
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  expect(creates).toBe(1);
  const loginUrl = new URL(page.url());
  expect(loginUrl.pathname).toBe("/login");
  const next = loginUrl.searchParams.get("next");
  if (!next || !/^\/orders\/[^/]+$/.test(next)) {
    throw new Error(`bad next after transitions: ${next} (url ${page.url()})`);
  }
  const jobId = next.replace("/orders/", "");

  // Every /login navigation the app produced carries the recorded order path,
  // never the raw home path — the guarantee does not depend on race luck.
  const navigations = await readNavigations(page);
  const loginNavigations = navigations.filter((url) => url.startsWith("/login"));
  expect(loginNavigations.length).toBeGreaterThan(0);
  for (const url of loginNavigations) {
    expect(decodeURIComponent(url)).toContain(`next=/orders/${jobId}`);
  }

  // After the login the same order opens for the top-up, without a new POST.
  await page.unroute(/\/api\/client\/jobs\/[^/]+\/input/);
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page).toHaveURL(new RegExp(`/orders/${jobId}$`), { timeout: 30_000 });
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
  const putResponse = page.waitForResponse(
    (r) => r.request().method() === "PUT" && /\/input\b/.test(r.url()),
  );
  await page
    .locator('input[aria-label="Файлы"]')
    .first()
    .setInputFiles([
      { name: "задание.pdf", mimeType: "application/octet-stream", buffer: Buffer.from("x") },
    ]);
  const put = await putResponse;
  expect(put.status()).toBe(401);
  await put.finished();

  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 15_000,
  });
  // The recorded return path is the top-up job page itself.
  await expect(page).toHaveURL(new RegExp(`login\\?next=%2Forders%2F${job.id}$`));

  await page.unroute(/\/api\/client\/jobs\/[^/]+\/input/);
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page).toHaveURL(new RegExp(`/orders/${job.id}$`), { timeout: 30_000 });
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
  let startedResolve: () => void = () => {};
  const started = new Promise<void>((resolve) => {
    startedResolve = resolve;
  });
  await page.route(/\/api\/client\/jobs\/[^/]+\/input/, async (route) => {
    if (route.request().method() !== "PUT") {
      await route.continue();
      return;
    }
    startedResolve();
    await gate;
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      body: UNAUTHORIZED,
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
  await started; // the PUT has really started before the session ends
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
  const bAvatar = page.getByRole("button", { name: /Меню пользователя Другой Клиент/ });
  await expect(bAvatar).toBeVisible();
  await pickPromise;

  // The old PUT is delivered now; B stays on /, no login form appears.
  const delivered = page.waitForResponse(
    (r) => r.request().method() === "PUT" && /\/input\b/.test(r.url()),
  );
  release();
  await (await delivered).finished();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toHaveCount(0);
  await expect(bAvatar).toBeVisible();
});
