import { randomUUID } from "node:crypto";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, request as playwrightRequest, test } from "@playwright/test";
import { apiContextAs, createJobViaApi, loginAdmin, waitForApiStatusInContext } from "./helpers";

// PR 6 admin panel (08-web-admin.md). Real Go server + worker + fake agents.
// Synthetic orders are prepared over the API as a client; every asserted
// action goes through the panel UI.

const base = process.env.E2E_BASE_URL ?? "";
const artifactDir = process.env.PR6_ARTIFACT_DIR ?? "./.artifacts/pr6";

async function saveAdminMockup(page: import("@playwright/test").Page) {
  const mockupPath = fileURLToPath(
    new URL("../../../docs/design/mockups/AdminReview.dc.html", import.meta.url),
  );
  const drawingPath = fileURLToPath(
    new URL("../../../docs/design/mockups/Drawing.dc.html", import.meta.url),
  );
  const [reviewHtml, drawingHtml] = await Promise.all([
    readFile(mockupPath, "utf8"),
    readFile(drawingPath, "utf8"),
  ]);
  const drawing = /<x-dc>([\s\S]*?)<\/x-dc>/.exec(drawingHtml)?.[1];
  if (!drawing) throw new Error("Drawing.dc.html has no x-dc content");
  const html = reviewHtml
    .replace(/<script[^>]*support\.js[^>]*><\/script>/, "")
    .replace(/<dc-import\s+name="Drawing"[^>]*><\/dc-import>/, drawing);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.setContent(html, { waitUntil: "domcontentloaded" });
  await page.addStyleTag({ content: "x-dc{display:block} helmet{display:none}" });
  await page.screenshot({
    path: path.join(artifactDir, "admin-review-mockup-1440.png"),
    fullPage: true,
  });
}

async function waitForAdminDetail(page: import("@playwright/test").Page, jobId: string) {
  let latest:
    | {
        title: string;
        status: string;
        agent_runs?: {
          id: string;
          agent: string;
          started_at: string;
          finished_at?: string | null;
          outcome?: string | null;
        }[];
        events?: { kind: string; data: Record<string, unknown> }[];
      }
    | undefined;
  await expect
    .poll(
      async () => {
        const response = await page.request.get(`/api/admin/jobs/${jobId}`);
        if (!response.ok()) return `http-${response.status()}`;
        latest = await response.json();
        return "ok";
      },
      { timeout: 30_000 },
    )
    .toBe("ok");
  if (!latest) throw new Error(`admin detail for ${jobId} was not read`);
  return latest;
}

async function navigateBetweenAdminCards(page: import("@playwright/test").Page, jobId: string) {
  await page
    .getByRole("navigation", { name: "Навигация пульта" })
    .getByRole("link", { name: "Заказы" })
    .click();
  await expect(page).toHaveURL(/\/admin\/jobs(?:\?.*)?$/);
  await page.locator(`a[href="/admin/jobs/${jobId}"]`).click();
  await expect(page).toHaveURL(new RegExp(`/admin/jobs/${jobId}$`));
}

test("admin login returns to the panel next; a client cannot open /admin", async ({ page }) => {
  // Deep link as anonymous: the server sends us to the login form with the
  // panel address as a local next.
  await page.goto("/admin/jobs");
  await expect(page).toHaveURL(/\/login\?next=%2Fadmin%2Fjobs/);

  // No admin request may run before the role is confirmed.
  const adminRequests: string[] = [];
  page.on("request", (req) => {
    if (req.url().includes("/api/admin/")) {
      adminRequests.push(req.url());
    }
  });

  // A client login must NOT enter the panel and must not loop.
  await page.getByLabel("Почта").fill("client@example.com");
  await page.getByLabel("Пароль").fill("client-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();
  await expect(page).toHaveURL(/\/$/);
  expect(adminRequests).toEqual([]);

  // Sign the client out before the admin logs in (one shared session cookie).
  await page.getByRole("button", { name: /Меню пользователя/ }).click();
  await page.getByRole("menuitem", { name: "Выйти" }).click();
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible();

  // The admin logs in and lands on the panel next.
  await loginAdmin(page, "/admin/jobs");
  await expect(page.getByRole("heading", { name: "Заказы" })).toBeVisible();

  // Direct API access is enforced by the server: client → 403, anonymous → 401.
  const clientApi = await apiContextAs(base, "client");
  expect((await clientApi.get("/api/admin/jobs")).status()).toBe(403);
  await clientApi.dispose();
  const anon = await playwrightRequest.newContext({ baseURL: base });
  expect((await anon.get("/api/admin/jobs")).status()).toBe(401);
  await anon.dispose();
});

test("a current admin API 401 shows login even when /me still returns 200", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Заказ для повторного входа администратора");
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  await loginAdmin(page, `/admin/jobs/${jobId}`);

  let forced401 = false;
  await page.route(`**/api/admin/jobs/${jobId}`, async (route) => {
    if (!forced401 && route.request().method() === "GET") {
      forced401 = true;
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: `{"error":"unauthorized"}`,
      });
      return;
    }
    await route.continue();
  });
  await page.reload();
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 30_000,
  });
  const url = new URL(page.url());
  expect(url.pathname).toBe("/login");
  expect(url.searchParams.get("next")).toBe(`/admin/jobs/${jobId}`);
  expect(url.searchParams.get("reauth")).toBe("1");
  // The logged-in cookie still produces /me 200. The explicit reauth intent
  // must keep the login form visible until the admin submits credentials.
  await page.getByLabel("Почта").fill("admin@example.com");
  await page.getByLabel("Пароль").fill("admin-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page).toHaveURL(new RegExp(`/admin/jobs/${jobId}$`), { timeout: 30_000 });
  const detail = await waitForAdminDetail(page, jobId);
  await expect(page.getByRole("heading", { name: detail.title })).toBeVisible();
  await clientApi.dispose();
});

test("late detail 200 and 401 for the previous card cannot replace or expire the current card", async ({
  page,
}) => {
  const clientApi = await apiContextAs(base, "client");
  const strangerApi = await apiContextAs(base, "stranger");
  const oldId = await createJobViaApi(clientApi, "Старая карточка для гонки");
  const currentId = await createJobViaApi(strangerApi, "Текущая карточка для гонки");
  await waitForApiStatusInContext(clientApi, oldId, "done", 180_000);
  await waitForApiStatusInContext(strangerApi, currentId, "done", 180_000);
  await loginAdmin(page);
  await page.locator(`a[href="/admin/jobs/${currentId}"]`).click();
  await expect(page.getByText("stranger@example.com")).toBeVisible();

  let heldKind: "200" | "401" | null = null;
  let startedResolve: (() => void) | undefined;
  let releaseResolve: (() => void) | undefined;
  let started = new Promise<void>(() => {});
  let release = new Promise<void>(() => {});
  const hold = (kind: "200" | "401") => {
    heldKind = kind;
    started = new Promise<void>((resolve) => {
      startedResolve = resolve;
    });
    release = new Promise<void>((resolve) => {
      releaseResolve = resolve;
    });
  };
  await page.route(`**/api/admin/jobs/${oldId}`, async (route) => {
    if (new URL(route.request().url()).pathname !== `/api/admin/jobs/${oldId}`) {
      await route.continue();
      return;
    }
    if (route.request().method() !== "GET" || !heldKind) {
      await route.continue();
      return;
    }
    const kind = heldKind;
    heldKind = null;
    console.log(`late-detail: hold started ${kind}`);
    const response = kind === "200" ? await route.fetch() : undefined;
    console.log(`late-detail: fetched response ${kind} ${response?.status() ?? "none"}`);
    startedResolve?.();
    await release;
    console.log(`late-detail: hold released ${kind}`);
    if (response) {
      await route.fulfill({ response });
    } else {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: '{"error":"unauthorized"}',
      });
    }
    console.log(`late-detail: route fulfilled ${kind}`);
  });

  const showCurrentCard = async () => {
    await navigateBetweenAdminCards(page, currentId);
    await expect(page.getByText("stranger@example.com")).toBeVisible();
  };

  hold("200");
  const old200 = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === `/api/admin/jobs/${oldId}` &&
      response.request().method() === "GET",
  );
  await navigateBetweenAdminCards(page, oldId);
  await started;
  await showCurrentCard();
  await page.evaluate(() => {
    const state = window as typeof window & { __pr6LateMutations?: string[] };
    state.__pr6LateMutations = [];
    new MutationObserver(() => state.__pr6LateMutations?.push(document.body.innerText)).observe(
      document.body,
      { childList: true, subtree: true, characterData: true },
    );
  });
  releaseResolve?.();
  const response200 = await old200;
  expect(response200.status()).toBe(200);
  expect(await response200.finished()).toBeNull();
  await expect(page).toHaveURL(new RegExp(`/admin/jobs/${currentId}$`));
  await expect(page.getByText("stranger@example.com")).toBeVisible();
  await expect(page.getByText("client@example.com")).toHaveCount(0);
  const oldTitleFlashed = await page.evaluate(
    () =>
      (window as typeof window & { __pr6LateMutations?: string[] }).__pr6LateMutations?.some(
        (text) => text.includes("client@example.com"),
      ) ?? false,
  );
  expect(oldTitleFlashed).toBe(false);

  hold("401");
  const old401 = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === `/api/admin/jobs/${oldId}` &&
      response.request().method() === "GET",
  );
  await navigateBetweenAdminCards(page, oldId);
  await started;
  await showCurrentCard();
  releaseResolve?.();
  const response401 = await old401;
  expect(response401.status()).toBe(401);
  expect(await response401.finished()).toBeNull();
  await expect(page).toHaveURL(new RegExp(`/admin/jobs/${currentId}$`));
  await expect(page.getByText("stranger@example.com")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toHaveCount(0);
  await clientApi.dispose();
  await strangerApi.dispose();
});

test("404 shows an empty missing-order state instead of the previous card", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Существующий заказ для проверки 404");
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  await loginAdmin(page, `/admin/jobs/${jobId}`);
  await expect(page.getByText("client@example.com")).toBeVisible();
  await page.goto("/admin/jobs/00000000-0000-4000-8000-000000000001");
  await expect(page.getByRole("heading", { name: "Заказ не найден" })).toBeVisible();
  await expect(page.getByText("client@example.com")).toHaveCount(0);
  await clientApi.dispose();
});

test("a late note mutation from the previous card cannot clear or leak into the current card", async ({
  page,
}) => {
  const clientApi = await apiContextAs(base, "client");
  const strangerApi = await apiContextAs(base, "stranger");
  const oldId = await createJobViaApi(clientApi, "Старая карточка для заметки");
  const currentId = await createJobViaApi(strangerApi, "Текущая карточка для заметки");
  await waitForApiStatusInContext(clientApi, oldId, "done", 180_000);
  await waitForApiStatusInContext(strangerApi, currentId, "done", 180_000);
  await loginAdmin(page);
  await page.locator(`a[href="/admin/jobs/${currentId}"]`).click();
  await expect(page.getByText("stranger@example.com")).toBeVisible();
  await navigateBetweenAdminCards(page, oldId);

  let startedResolve: (() => void) | undefined;
  let releaseResolve: (() => void) | undefined;
  const started = new Promise<void>((resolve) => {
    startedResolve = resolve;
  });
  const release = new Promise<void>((resolve) => {
    releaseResolve = resolve;
  });
  const noteFromOld = "Заметка старой карточки, ответ задержан";
  await page.route(`**/api/admin/jobs/${oldId}/notes`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    const response = await route.fetch();
    startedResolve?.();
    await release;
    await route.fulfill({ response });
  });
  const noteResponse = page.waitForResponse(
    (response) =>
      response.url().includes(`/api/admin/jobs/${oldId}/notes`) &&
      response.request().method() === "POST",
  );
  await page.getByLabel("Заметка").fill(noteFromOld);
  await page.getByRole("button", { name: "Добавить" }).click();
  await started;

  await navigateBetweenAdminCards(page, currentId);
  await expect(page.getByText("stranger@example.com")).toBeVisible();
  const currentDraft = "Черновик заметки текущей карточки";
  await page.getByLabel("Заметка").fill(currentDraft);
  await page.evaluate(() => {
    const state = window as typeof window & { __pr6MutationBodies?: string[] };
    state.__pr6MutationBodies = [];
    new MutationObserver(() => state.__pr6MutationBodies?.push(document.body.innerText)).observe(
      document.body,
      { childList: true, subtree: true, characterData: true },
    );
  });
  releaseResolve?.();
  const response = await noteResponse;
  expect(response.status()).toBe(201);
  expect(await response.finished()).toBeNull();
  await expect(page.getByLabel("Заметка")).toHaveValue(currentDraft);
  await expect(page.getByText(noteFromOld)).toHaveCount(0);
  const leaked = await page.evaluate(
    () =>
      (window as typeof window & { __pr6MutationBodies?: string[] }).__pr6MutationBodies?.some(
        (text) => text.includes("Заметка старой карточки, ответ задержан"),
      ) ?? false,
  );
  expect(leaked).toBe(false);
  await clientApi.dispose();
  await strangerApi.dispose();
});

test("a 409 cancel response keeps the active order visible without a false success", async ({
  page,
}) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Заказ с конфликтом отмены #hang");
  await loginAdmin(page, `/admin/jobs/${jobId}`);
  await expect.poll(async () => (await waitForAdminDetail(page, jobId)).status).toBe("running");
  let conflict = true;
  await page.route(`**/api/admin/jobs/${jobId}/cancel`, async (route) => {
    if (conflict) {
      conflict = false;
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: '{"error":{"code":"conflict","message":"Состояние изменилось"}}',
      });
    } else {
      await route.continue();
    }
  });
  await page.getByRole("button", { name: "Отменить" }).first().click();
  await page.getByRole("button", { name: "Отменить" }).last().click();
  await expect(page.getByRole("button", { name: "Отменить" })).toBeVisible();
  expect((await waitForAdminDetail(page, jobId)).status).toBe("running");
  await expect(page.getByText("Заказ отменён")).toHaveCount(0);
  await clientApi.post(`/api/client/jobs/${jobId}/cancel`);
  await waitForApiStatusInContext(clientApi, jobId, "canceled", 60_000);
  await clientApi.dispose();
});

test("detail polling stops when leaving the card and after logout", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Заказ для проверки очистки polling");
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  await loginAdmin(page);
  await page.clock.install({ time: new Date("2026-10-08T10:00:00Z") });
  await page.locator(`a[href="/admin/jobs/${jobId}"]`).click();
  await expect(page.getByText("client@example.com")).toBeVisible();
  let detailRequests = 0;
  page.on("request", (request) => {
    if (request.url().includes(`/api/admin/jobs/${jobId}`)) detailRequests += 1;
  });
  await page
    .getByRole("navigation", { name: "Навигация пульта" })
    .getByRole("link", { name: "Заказы" })
    .click();
  await expect(page).toHaveURL(/\/admin\/jobs$/);
  const afterLeaving = detailRequests;
  await page.clock.fastForward(25_000);
  expect(detailRequests).toBe(afterLeaving);
  await page.locator(`a[href="/admin/jobs/${jobId}"]`).click();
  await expect(page.getByText("client@example.com")).toBeVisible();
  await page.getByRole("button", { name: /Выйти/ }).click();
  await page.getByRole("menuitem", { name: "Выйти" }).click();
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible();
  const afterLogout = detailRequests;
  await page.clock.fastForward(25_000);
  expect(detailRequests).toBe(afterLogout);
  await clientApi.dispose();
});

test("orders table: columns, joint filters, search and client filter", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  const strangerApi = await apiContextAs(base, "stranger");
  const clientUser = (await (await clientApi.get("/api/auth/me")).json()).user as { id: string };
  const aliceId = clientUser.id;
  const clientJobId = await createJobViaApi(clientApi, "Заказ клиента А: расчёт балки");
  const strangerJobId = await createJobViaApi(strangerApi, "Заказ клиента Б: чертёж вала");
  const attentionId = await createJobViaApi(clientApi, "Заказ клиента А: внимание #fail-verify");
  await waitForApiStatusInContext(clientApi, attentionId, "delayed", 120_000);

  await loginAdmin(page);
  await expect(page.getByRole("heading", { name: "Заказы" })).toBeVisible();

  // Both synthetic clients appear with their emails.
  await expect(page.getByText("client@example.com").first()).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("stranger@example.com").first()).toBeVisible();

  // Search by email returns only Stranger orders, including earlier orders
  // left in the shared harness database by preceding tests.
  await page.getByLabel("Поиск").fill("stranger@example.com");
  await expect(page).toHaveURL(/q=/);
  const strangerCells = page.getByRole("cell", { name: "stranger@example.com" });
  await expect.poll(() => strangerCells.count()).toBeGreaterThan(0);
  await expect(page.getByText("client@example.com")).toHaveCount(0);

  // A unique, unsubmitted title isolates this test's row from stranger jobs
  // created by earlier tests in the shared harness database.
  const uniqueSearchToken = `search-${randomUUID()}`;
  const uniqueSearchResponse = await strangerApi.post("/api/client/jobs", {
    data: { prompt: uniqueSearchToken },
  });
  expect(uniqueSearchResponse.ok()).toBe(true);
  const uniqueSearchId = ((await uniqueSearchResponse.json()) as { id: string }).id;
  await page.getByLabel("Поиск").fill(uniqueSearchToken);
  const uniqueSearchRow = page.locator(`a[href^="/admin/jobs/${uniqueSearchId}"]`);
  await expect(uniqueSearchRow).toBeVisible();
  await expect(uniqueSearchRow).toHaveCount(1);

  // Clearing the search restores both.
  await page.getByLabel("Поиск").fill("");
  await expect(page.getByText("client@example.com").first()).toBeVisible();

  // Status, attention, search and exact client id apply together and remain
  // in the card URL and return link.
  await page.goto(
    `/admin/jobs?client=${aliceId}&status=failed&attention=1&q=${encodeURIComponent("client@example.com")}`,
  );
  await expect(page.getByLabel("Только требующие внимания")).toBeChecked();
  await expect(page.getByLabel("Статус")).toHaveValue("failed");
  await expect(page.getByText("stranger@example.com")).toHaveCount(0);
  const attentionRow = page.locator(`a[href^="/admin/jobs/${attentionId}?"]`);
  await expect(attentionRow).toBeVisible();
  await expect(attentionRow).toHaveCount(1);
  await expect(page.locator(`a[href^="/admin/jobs/${clientJobId}?"]`)).toHaveCount(0);
  await expect(page.locator(`a[href^="/admin/jobs/${strangerJobId}?"]`)).toHaveCount(0);
  await attentionRow.click();
  await expect(page).toHaveURL(/client=.*status=failed.*attention=1.*q=/);
  await page.getByRole("link", { name: "← Заказы" }).click();
  await expect(page).toHaveURL(/client=.*status=failed.*attention=1.*q=/);

  // The exact-client filter (from the clients screen) narrows by id.
  await page.goto(`/admin/jobs?client=${aliceId}`);
  await expect(page.getByText("client@example.com").first()).toBeVisible();
  await expect(page.getByText("stranger@example.com")).toHaveCount(0);

  // The client screen links to the same filtered screen.
  await page.goto("/admin/clients");
  await expect(page.getByRole("heading", { name: "Клиенты" })).toBeVisible();
  await page.getByRole("link", { name: "client@example.com" }).click();
  await expect(page).toHaveURL(new RegExp(`client=${aliceId}`));
  await expect(page.getByText("client@example.com").first()).toBeVisible();

  await clientApi.dispose();
  await strangerApi.dispose();
});

test("review: live steps, draft/version switch, changed boxes, admin answer", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Сделай практическую работу #ask");

  await loginAdmin(page);
  await page.goto(`/admin/jobs/${jobId}`);

  // The order header shows the client and state.
  const initialDetail = await waitForAdminDetail(page, jobId);
  await expect(page.getByRole("heading", { name: initialDetail.title })).toBeVisible({
    timeout: 30_000,
  });
  await expect(page.getByText("client@example.com").first()).toBeVisible();

  // The agent run appears in the trace with a raw-log link.
  await expect(page.getByRole("link", { name: "сырой лог" }).first()).toBeVisible({
    timeout: 30_000,
  });

  // Wait for the question, then answer as the admin.
  await waitForApiStatusInContext(clientApi, jobId, "needs_input");
  await expect(page.getByLabel("Ответить за клиента")).toBeVisible({ timeout: 30_000 });
  await page.getByLabel("Ответить за клиента").fill("Нагрузка q = 10 кН/м, вариант 14");
  await page.getByRole("button", { name: "Ответить" }).click();

  // The order completes; a version with documents appears and the admin sees
  // the draft/version switch and the note about changed boxes.
  await waitForApiStatusInContext(clientApi, jobId, "done");
  const answered = await waitForAdminDetail(page, jobId);
  expect(
    answered.events?.some((event) => event.kind === "answered" && event.data.by === "admin"),
  ).toBe(true);
  await expect(page.getByRole("tablist", { name: "Черновик и версии" })).toBeVisible({
    timeout: 60_000,
  });
  await expect(page.getByRole("checkbox", { name: "Показать правки" })).toBeVisible();
  await expect(page.getByRole("tablist", { name: "Документы" })).toBeVisible();
  await page.getByRole("tab", { name: "Версия 1" }).click();
  await expect(page.locator(".sl-admin-box--changed").first()).toBeVisible({ timeout: 30_000 });
  await page.getByRole("tab", { name: "Черновик" }).click();
  await expect(page.getByRole("tab", { name: "Черновик" })).toHaveAttribute(
    "aria-selected",
    "true",
  );

  await mkdir(artifactDir, { recursive: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("heading", { name: answered.title })).toBeVisible();
  await expect(page.getByRole("tablist", { name: "Черновик и версии" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Скачать файл" })).toBeVisible();
  const mobilePanelBounds = await page.evaluate(() => {
    const trace = document.querySelector('[aria-label="Трейс агентов"]');
    const sheets = document.querySelector('[aria-label="Листы комплекта"]');
    const steps = document.querySelector('[aria-label="Шаги трейса"]');
    if (!trace || !sheets || !steps) return null;
    return {
      traceBottom: trace.getBoundingClientRect().bottom,
      sheetsTop: sheets.getBoundingClientRect().top,
      stepsClientHeight: steps.clientHeight,
      stepsScrollHeight: steps.scrollHeight,
    };
  });
  if (!mobilePanelBounds) throw new Error("mobile admin panels were not found");
  expect(mobilePanelBounds.sheetsTop).toBeGreaterThanOrEqual(mobilePanelBounds.traceBottom);
  expect(mobilePanelBounds.stepsClientHeight).toBeLessThanOrEqual(420);
  expect(mobilePanelBounds.stepsScrollHeight).toBeGreaterThan(mobilePanelBounds.stepsClientHeight);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1),
  ).toBe(true);
  await page.screenshot({
    path: path.join(artifactDir, "admin-review-live-390.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.screenshot({
    path: path.join(artifactDir, "admin-review-live-1440.png"),
    fullPage: true,
  });
  await saveAdminMockup(page);

  await clientApi.dispose();
});

test("a new agent run arriving during a pending history page is backfilled", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Синтетическая работа для смены набора runs #ask");
  await waitForApiStatusInContext(clientApi, jobId, "needs_input", 120_000);
  await loginAdmin(page, `/admin/jobs/${jobId}`);

  let startedResolve: () => void = () => {};
  let releaseResolve: () => void = () => {};
  const started = new Promise<void>((resolve) => {
    startedResolve = resolve;
  });
  const release = new Promise<void>((resolve) => {
    releaseResolve = resolve;
  });
  let held = false;
  await page.route(
    /\/api\/admin\/jobs\/[^/]+\/runs\/[^/]+\/steps\?after_seq=0(?:&|$)/,
    async (route) => {
      if (!held) {
        held = true;
        startedResolve();
        await release;
      }
      await route.continue();
    },
  );
  await page.reload();
  await started;
  await expect(page.getByLabel("Ответить за клиента")).toBeVisible();
  await page.getByLabel("Ответить за клиента").fill("Нагрузка 10 кН/м, вариант 14");
  await page.getByRole("button", { name: "Ответить" }).click();
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  const changedRunSet = await waitForAdminDetail(page, jobId);
  expect(changedRunSet.agent_runs?.length ?? 0).toBeGreaterThanOrEqual(2);
  releaseResolve();

  const expected: { summary: string }[] = [];
  let expectedCount = 0;
  for (const run of changedRunSet.agent_runs ?? []) {
    let afterSeq = 0;
    for (;;) {
      const response = await page.request.get(
        `/api/admin/jobs/${jobId}/runs/${run.id}/steps?after_seq=${afterSeq}`,
      );
      expect(response.ok()).toBeTruthy();
      const body = (await response.json()) as { steps: { seq: number; summary: string }[] };
      expectedCount += body.steps.length;
      for (const step of body.steps) expected.push({ summary: step.summary });
      if (body.steps.length < 500) break;
      afterSeq = body.steps.at(-1)?.seq ?? afterSeq;
    }
  }
  await expect
    .poll(async () => page.locator('[aria-label="Шаги трейса"] li').count(), { timeout: 30_000 })
    .toBe(expectedCount);
  for (const step of expected) {
    await expect(
      page.locator('[aria-label="Шаги трейса"]').getByText(step.summary, { exact: true }).first(),
    ).toBeVisible();
  }
  await page.reload();
  await expect(page.locator('[aria-label="Шаги трейса"] li')).toHaveCount(expectedCount);
  await clientApi.dispose();
});

test("retry after #fail-verify-twice reaches done; #fail-verify stays failed", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  await loginAdmin(page);

  // A hard failure: stays failed with attention.
  const hardId = await createJobViaApi(clientApi, "Сделай практическую работу #fail-verify");
  await waitForApiStatusInContext(clientApi, hardId, "delayed", 120_000);
  const hardFailed = await waitForAdminDetail(page, hardId);
  expect(hardFailed.agent_runs?.at(-1)?.finished_at).toBeTruthy();

  // A failure that admin retry resolves.
  const retryId = await createJobViaApi(clientApi, "Сделай практическую работу #fail-verify-twice");
  await waitForApiStatusInContext(clientApi, retryId, "delayed", 120_000);
  await expect
    .poll(async () => (await waitForAdminDetail(page, retryId)).agent_runs?.length ?? 0, {
      timeout: 120_000,
    })
    .toBeGreaterThanOrEqual(2);

  // The hard failure: retry is offered; the panel updates after the action.
  await page.goto(`/admin/jobs/${hardId}`);
  await expect(page.getByRole("button", { name: "Повторить" })).toBeVisible({ timeout: 30_000 });
  await page.getByRole("button", { name: "Повторить" }).click();
  // It fails again (second failure already consumed): still not done.
  await waitForApiStatusInContext(clientApi, hardId, "delayed", 120_000);

  // The twice-failure order reaches done via the admin retry.
  await page.goto(`/admin/jobs/${retryId}`);
  await expect(page.getByRole("button", { name: "Повторить" })).toBeVisible({ timeout: 30_000 });
  await page.getByRole("button", { name: "Повторить" }).click();
  await waitForApiStatusInContext(clientApi, retryId, "done", 120_000);

  await clientApi.dispose();
});

test("admin shows a real client remark on its source version and the matching sheet", async ({
  page,
}) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Синтетическая работа для замечания");
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  const clientDetailResponse = await clientApi.get(`/api/client/jobs/${jobId}`);
  expect(clientDetailResponse.ok()).toBeTruthy();
  const clientDetail = (await clientDetailResponse.json()) as {
    versions: { version: number; documents: { id: string; page_count: number; title: string }[] }[];
  };
  const sourceDocument = clientDetail.versions.find((version) => version.version === 1)
    ?.documents[0];
  expect(sourceDocument).toBeTruthy();
  expect(sourceDocument?.page_count).toBeGreaterThan(0);

  const revisionResponse = await clientApi.post(`/api/client/jobs/${jobId}/revisions`, {
    multipart: {
      data: JSON.stringify({
        comment: "Проверьте выделенное место на первом листе",
        remarks: [
          {
            document_id: sourceDocument?.id,
            page: 1,
            x: 0.12,
            y: 0.18,
            w: 0.24,
            h: 0.08,
            text: "Исправить формулу в этом месте",
          },
        ],
      }),
      files: {
        name: "appendix/ новые данные.bin",
        mimeType: "application/octet-stream",
        buffer: Buffer.from([5, 0, 15, 255]),
      },
    },
  });
  expect(revisionResponse.status()).toBe(201);
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);

  const plainJobId = await createJobViaApi(clientApi, "Синтетическая работа без доработки");
  await waitForApiStatusInContext(clientApi, plainJobId, "done", 180_000);
  const plainDetailResponse = await clientApi.get(`/api/client/jobs/${plainJobId}`);
  expect(plainDetailResponse.ok()).toBeTruthy();
  const plainDetail = (await plainDetailResponse.json()) as {
    versions: { version: number; documents: { title: string }[] }[];
  };
  expect(plainDetail.versions.map((version) => version.version)).toEqual([1]);
  const plainDocument = plainDetail.versions.find((version) => version.version === 1)?.documents[0];
  expect(plainDocument?.title).toBeTruthy();

  await loginAdmin(page, `/admin/jobs/${jobId}`);
  const revisedAdminDetail = await waitForAdminDetail(page, jobId);
  await expect(page.getByRole("heading", { name: revisedAdminDetail.title })).toBeVisible();
  await expect(page.getByText("Проверьте выделенное место на первом листе")).toBeVisible();
  await expect(page.getByText("Исправить формулу в этом месте")).toBeVisible();
  await expect(
    page.getByRole("link", { name: "input/revision-2/appendix/ новые данные.bin" }),
  ).toBeVisible();
  await page.getByRole("button", { name: /стр\. 1, замечание/ }).click();
  await expect(page.getByRole("tab", { name: "Версия 1" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.locator(".sl-admin-box--remark")).toBeVisible();

  // Select A's newer version, then use SPA links to open B. B only has v1;
  // its sheets must not inherit A's selected v2 or document state.
  await page.getByRole("tab", { name: "Версия 2" }).click();
  await expect(page.getByRole("tab", { name: "Версия 2" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  // The real fake-agent output includes a saved draft snapshot even after v1.
  // Force the documented no-draft detail shape so this route change exercises
  // the exact edge where an old draft jump could select an empty document set.
  let plainDetailFixture: Record<string, unknown> | undefined;
  await page.context().route(
    (url) => url.pathname === `/api/admin/jobs/${plainJobId}`,
    async (route) => {
      const response = await route.fetch();
      const body = (await response.json()) as Record<string, unknown>;
      plainDetailFixture = { ...body, draft: null };
      await route.fulfill({ response, json: { ...body, draft: null } });
    },
  );
  await page.context().route(
    (url) => url.pathname === `/api/admin/jobs/${plainJobId}/stream`,
    (route) => route.abort("failed"),
  );
  const plainDetailResponsePromise = page.waitForResponse((response) => {
    const request = response.request();
    return (
      request.method() === "GET" &&
      new URL(response.url()).pathname === `/api/admin/jobs/${plainJobId}`
    );
  });
  await navigateBetweenAdminCards(page, plainJobId);
  const browserPlainDetailResponse = await plainDetailResponsePromise;
  expect(browserPlainDetailResponse.ok()).toBeTruthy();
  const plainAdminDetail = (await browserPlainDetailResponse.json()) as {
    title: string;
    status: string;
    draft: unknown;
  };
  expect(plainDetailFixture).toBeTruthy();
  expect(plainAdminDetail.draft).toBeNull();
  await expect(page.getByRole("heading", { name: plainAdminDetail.title })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Версия 1" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.getByRole("tab", { name: "Черновик" })).toHaveCount(0);
  await expect(page.getByRole("tab", { name: plainDocument?.title ?? "" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Версия 2" })).toHaveCount(0);
  const currentSheet = page.getByRole("img", { name: "Лист 1" });
  await expect(currentSheet).toHaveAttribute("src", new RegExp(plainJobId));
  await expect(currentSheet).not.toHaveAttribute("src", new RegExp(jobId));
  await expect(currentSheet).not.toHaveAttribute("src", new RegExp(sourceDocument?.id ?? ""));
  await clientApi.dispose();
});

test("cancel, attention and a saved note survive reload", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  await loginAdmin(page);
  const noteId = await createJobViaApi(clientApi, "Сделай практическую работу #fail-verify");
  await waitForApiStatusInContext(clientApi, noteId, "delayed", 120_000);
  await expect
    .poll(
      async () => (await waitForAdminDetail(page, noteId)).agent_runs?.at(-1)?.finished_at ?? null,
      { timeout: 30_000 },
    )
    .toBeTruthy();
  const cancelId = await createJobViaApi(clientApi, "Сделай практическую работу #hang");

  // Do not race the queue: confirm the fake agent has actually started and
  // that its run is still unfinished before asking the panel to cancel it.
  await expect
    .poll(async () => (await waitForAdminDetail(page, cancelId)).status, { timeout: 30_000 })
    .toBe("running");
  const started = await waitForAdminDetail(page, cancelId);
  expect(started.agent_runs?.at(-1)?.started_at).toBeTruthy();
  expect(started.agent_runs?.at(-1)?.finished_at ?? null).toBeNull();

  // Cancel a hanging order; the panel waits for the server's canceled state.
  try {
    await page.goto(`/admin/jobs/${cancelId}`);
    const cancelResponse = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === `/api/admin/jobs/${cancelId}/cancel` &&
        response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Отменить" }).first().click();
    await page.getByRole("button", { name: "Отменить" }).last().click();
    const response = await cancelResponse;
    await waitForApiStatusInContext(clientApi, cancelId, "canceled", 60_000);
  } finally {
    // Do not strand the single worker if an assertion in this scenario fails.
    await page.request.post(`/api/admin/jobs/${cancelId}/cancel`).catch(() => undefined);
  }

  // Clear attention and write a note; both reload-safely.
  await page.goto(`/admin/jobs/${noteId}`);
  await expect(page.getByRole("button", { name: "Снять флаг внимания" })).toBeVisible({
    timeout: 30_000,
  });
  await page.getByRole("button", { name: "Снять флаг внимания" }).click();
  await page.getByLabel("Заметка").fill("Проверил вручную: расчёт верный");
  await page.getByRole("button", { name: "Добавить" }).click();
  await expect(page.getByText("Проверил вручную: расчёт верный")).toBeVisible();

  await page.reload();
  await expect(page.getByText("Проверил вручную: расчёт верный")).toBeVisible({ timeout: 30_000 });

  await clientApi.dispose();
});

test("clients and workers screens show real fields", async ({ page }) => {
  await loginAdmin(page);

  await page.goto("/admin/workers");
  await expect(page.getByRole("heading", { name: "Воркеры" })).toBeVisible();
  // The harness worker is online while it runs.
  await expect(page.getByText("e2e-pw")).toBeVisible({ timeout: 30_000 });

  await page.goto("/admin/clients");
  await expect(page.getByRole("heading", { name: "Клиенты" })).toBeVisible();
  await expect(page.getByText("client@example.com")).toBeVisible();
});

test("real agent steps stream over SSE while the long trace is still running", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  await loginAdmin(page);
  await page.addInitScript(() => {
    const state = window as typeof window & { __pr6SSESteps?: number };
    state.__pr6SSESteps = 0;
    const original = EventSource.prototype.addEventListener;
    EventSource.prototype.addEventListener = function (type, callback, options) {
      if (type !== "steps" || typeof callback !== "function") {
        return original.call(this, type, callback, options);
      }
      const wrapped: EventListener = function (this: EventSource, event: Event) {
        state.__pr6SSESteps = (state.__pr6SSESteps ?? 0) + 1;
        callback.call(this, event);
      };
      return original.call(this, type, wrapped, options);
    };
  });

  const jobId = await createJobViaApi(clientApi, "Синтетическая работа для live SSE #longtrace");
  await expect
    .poll(async () => (await waitForAdminDetail(page, jobId)).status, { timeout: 30_000 })
    .toBe("running");
  await page.goto(`/admin/jobs/${jobId}`);
  const liveDetail = await waitForAdminDetail(page, jobId);
  await expect(page.getByRole("heading", { name: liveDetail.title })).toBeVisible();
  await expect(page.locator('[aria-label="Шаги трейса"] li').first()).toBeVisible({
    timeout: 30_000,
  });
  await expect
    .poll(
      () =>
        page.evaluate(
          () => (window as typeof window & { __pr6SSESteps?: number }).__pr6SSESteps ?? 0,
        ),
      { timeout: 30_000 },
    )
    .toBeGreaterThan(0);
  const duringRun = await waitForAdminDetail(page, jobId);
  expect(duringRun.status).toBe("running");
  expect(duringRun.agent_runs?.at(-1)?.finished_at ?? null).toBeNull();

  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  const finished = await waitForAdminDetail(page, jobId);
  expect(finished.agent_runs?.at(-1)?.finished_at).toBeTruthy();
  await clientApi.dispose();
});

test("admin deep link survives reload and trace history is complete after SSE reconnect", async ({
  page,
}) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Синтетическая работа #longtrace");
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  // Hold the first page of historical steps, then force an EventSource
  // disconnect/reconnect before releasing that response. These are explicit
  // request latches; no fixed delay is used to manufacture the race.
  let firstStepsStartedResolve: () => void = () => {};
  let releaseFirstStepsResolve: () => void = () => {};
  const firstStepsStarted = new Promise<void>((resolve) => {
    firstStepsStartedResolve = resolve;
  });
  const releaseFirstSteps = new Promise<void>((resolve) => {
    releaseFirstStepsResolve = resolve;
  });
  let heldFirstSteps = false;
  await page.route(
    /\/api\/admin\/jobs\/[^/]+\/runs\/[^/]+\/steps\?after_seq=0(?:&|$)/,
    async (route) => {
      if (!heldFirstSteps) {
        heldFirstSteps = true;
        firstStepsStartedResolve();
        await releaseFirstSteps;
      }
      await route.continue();
    },
  );
  let streams = 0;
  await page.route(`**/api/admin/jobs/${jobId}/stream`, async (route) => {
    streams += 1;
    if (streams === 1) {
      await route.abort("failed");
      return;
    }
    await route.continue();
  });
  await loginAdmin(page, `/admin/jobs/${jobId}`);
  const initialDetail = await waitForAdminDetail(page, jobId);
  await expect(page.getByRole("heading", { name: initialDetail.title })).toBeVisible();
  await firstStepsStarted;
  await expect.poll(() => streams, { timeout: 20_000 }).toBeGreaterThanOrEqual(2);
  releaseFirstStepsResolve();

  const steps = page.locator('[aria-label="Шаги трейса"] li');
  await expect(steps.first()).toContainText("Шаг 1");
  const detail = await waitForAdminDetail(page, jobId);
  const codexRun = detail.agent_runs?.find((run) => run.agent === "codex");
  expect(codexRun).toBeTruthy();
  const codexPages: { seq: number; summary: string; payload: unknown }[][] = [];
  let afterSeq = 0;
  for (;;) {
    const response = await page.request.get(
      `/api/admin/jobs/${jobId}/runs/${codexRun.id}/steps?after_seq=${afterSeq}`,
    );
    expect(response.ok()).toBeTruthy();
    const body = (await response.json()) as {
      steps: { seq: number; summary: string; payload: unknown }[];
    };
    expect(body.steps.length).toBeLessThanOrEqual(500);
    codexPages.push(body.steps);
    if (body.steps.length < 500) break;
    afterSeq = body.steps.at(-1)?.seq ?? afterSeq;
  }
  const codexSteps = codexPages.flat();
  expect(codexPages.length).toBeGreaterThanOrEqual(4);
  expect(codexSteps.length).toBeGreaterThanOrEqual(2000);
  const codexLast = codexSteps.at(-1);
  expect(codexLast).toBeTruthy();
  const lastSyntheticStep = codexSteps.find((step) => step.summary.includes("Шаг 2000"));
  expect(lastSyntheticStep?.payload).toBeTruthy();

  await steps.first().evaluate((el) => {
    const scroller = el.closest('[aria-label="Шаги трейса"]');
    if (scroller) scroller.scrollTop = scroller.scrollHeight;
  });
  await expect(steps.filter({ hasText: "Шаг 2000" })).toHaveCount(1);
  const count = await steps.count();
  const elapsedStart = Date.now();
  await page.getByLabel("Тип").selectOption("reasoning");
  await expect(steps.filter({ hasText: "Шаг 2000" })).toHaveCount(1);
  const filterMs = Date.now() - elapsedStart;
  console.log(
    `PR6 trace evidence: codexSteps=${codexSteps.length}, pages=${codexPages.length}, lastSeq=${codexLast?.seq}, domRows=${count}, filterMs=${filterMs}`,
  );
  expect(count).toBeLessThan(500);

  const lastSyntheticRow = steps.filter({ hasText: "Шаг 2000" }).last();
  await lastSyntheticRow.scrollIntoViewIfNeeded();
  await lastSyntheticRow.getByRole("button").click();
  await expect(lastSyntheticRow.locator("pre")).toHaveText("{}");
  await page.reload();
  const reloadedDetail = await waitForAdminDetail(page, jobId);
  await expect(page.getByRole("heading", { name: reloadedDetail.title })).toBeVisible();
  await page.locator('[aria-label="Шаги трейса"]').evaluate((el) => {
    el.scrollTop = el.scrollHeight;
  });
  await expect(
    page.locator('[aria-label="Шаги трейса"] li').filter({ hasText: "Шаг 2000" }),
  ).toHaveCount(1);
  await expect(
    page.locator(`[aria-label="Шаги трейса"] [data-seq="${codexLast?.seq}"]`).first(),
  ).toBeVisible();
  await clientApi.dispose();
});

test("admin source input is downloadable with exact bytes through the panel", async ({ page }) => {
  const clientApi = await apiContextAs(base, "client");
  const payload = Buffer.from([0, 1, 2, 32, 255, 13, 10, 42]);
  const jobId = await createJobViaApi(clientApi, "Синтетическая работа с исходником", {
    name: "методичка/ исходные данные.bin",
    body: payload,
  });
  const sourceDetail = await clientApi.get(`/api/client/jobs/${jobId}`);
  expect(sourceDetail.ok()).toBe(true);
  const sourceFiles = (await sourceDetail.json()).input_files as { path: string }[];
  expect(sourceFiles.map((file) => file.path)).toContain("методичка/ исходные данные.bin");
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  await loginAdmin(page, `/admin/jobs/${jobId}`);
  const detail = await waitForAdminDetail(page, jobId);
  await expect(page.getByRole("heading", { name: detail.title })).toBeVisible();
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("link", { name: "методичка/ исходные данные.bin" }).click();
  const download = await downloadPromise;
  const downloaded = await download.createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of downloaded) chunks.push(Buffer.from(chunk));
  expect(Buffer.concat(chunks)).toEqual(payload);
  await clientApi.dispose();
});

test("a steps API 401 requests admin reauthentication while /me remains successful", async ({
  page,
}) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Заказ для истечения сессии трейса #longtrace");
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  await loginAdmin(page, `/admin/jobs/${jobId}`);

  let forced401 = false;
  await page.route(/\/api\/admin\/jobs\/[^/]+\/runs\/[^/]+\/steps\?/, async (route) => {
    if (!forced401 && route.request().method() === "GET") {
      forced401 = true;
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: `{"error":"unauthorized"}`,
      });
      return;
    }
    await route.continue();
  });
  await page.reload();
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible({
    timeout: 30_000,
  });
  const url = new URL(page.url());
  expect(url.pathname).toBe("/login");
  expect(url.searchParams.get("next")).toBe(`/admin/jobs/${jobId}`);
  expect(url.searchParams.get("reauth")).toBe("1");
  await page.getByLabel("Почта").fill("admin@example.com");
  await page.getByLabel("Пароль").fill("admin-password-123");
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page).toHaveURL(new RegExp(`/admin/jobs/${jobId}$`), { timeout: 30_000 });
  await expect(page.locator('[aria-label="Шаги трейса"]').getByText("Шаг 2000")).toBeVisible({
    timeout: 60_000,
  });
  await clientApi.dispose();
});

test("a failed steps history request exposes a retry that restores trace without a new job", async ({
  page,
}) => {
  const clientApi = await apiContextAs(base, "client");
  const jobId = await createJobViaApi(clientApi, "Заказ для повтора загрузки трейса #longtrace");
  await waitForApiStatusInContext(clientApi, jobId, "done", 180_000);
  await loginAdmin(page, `/admin/jobs/${jobId}`);

  let failedOnce = false;
  await page.route(/\/api\/admin\/jobs\/[^/]+\/runs\/[^/]+\/steps\?/, async (route) => {
    if (!failedOnce && route.request().method() === "GET") {
      failedOnce = true;
      await route.abort("failed");
      return;
    }
    await route.continue();
  });
  await page.reload();
  const alert = page.getByRole("alert");
  await expect(alert).toContainText("Не удалось загрузить историю шагов", { timeout: 30_000 });
  await alert.getByRole("button", { name: "Повторить" }).click();
  await expect(page.locator('[aria-label="Шаги трейса"]').getByText("Шаг 2000")).toBeVisible({
    timeout: 60_000,
  });
  // The job remains the already completed synthetic order; retry only re-runs
  // the paged history request and never submits or restarts the order.
  expect((await waitForAdminDetail(page, jobId)).status).toBe("done");
  await clientApi.dispose();
});
