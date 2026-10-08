import { type Page, expect } from "@playwright/test";
import { testUsers } from "../harness.mjs";

// Shared helpers for the PR 5 cabinet scenarios. All flows go through the
// real UI (09-tasks.md); the API is used only to prepare state.

export async function login(page: Page, userKey: "client" | "stranger" | "admin" = "client") {
  const user = testUsers[userKey];
  if (userKey === "admin") {
    await loginAdmin(page);
    return;
  }
  await page.goto("/login");
  await page.getByLabel("Почта").fill(user.email);
  await page.getByLabel("Пароль").fill(user.password);
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();
}

/**
 * Signs in as the admin through the shared login form and lands in the panel.
 * The client SPA follows a local /admin next with a full navigation, so the
 * page ends up in the admin bundle (08-web-admin.md).
 */
export async function loginAdmin(page: Page, next = "/admin/jobs") {
  const user = testUsers.admin;
  await page.goto(`/login?next=${encodeURIComponent(next)}`);
  await page.getByLabel("Почта").fill(user.email);
  await page.getByLabel("Пароль").fill(user.password);
  await page.getByRole("button", { name: "Войти" }).click();
  await page.waitForURL(/\/admin\//, { timeout: 30_000 });
}

export async function logout(page: Page) {
  await page.getByRole("button", { name: /Меню пользователя/ }).click();
  await page.getByRole("menuitem", { name: "Выйти" }).click();
  await expect(page.getByRole("heading", { name: "Вход в studlance" })).toBeVisible();
}

/** Creates an order through the real form and returns the job page URL. */
export async function createOrder(
  page: Page,
  prompt: string,
  files: { dir?: string; paths?: string[] } = {},
) {
  await page.goto("/");
  const textarea = page.getByRole("textbox", { name: "Запрос" });
  await textarea.fill(prompt);
  if (files.dir) {
    const folderInput = page.locator('input[type="file"][webkitdirectory]');
    await folderInput.setInputFiles(files.dir);
  }
  if (files.paths) {
    const fileInput = page.locator('input[type="file"]:not([webkitdirectory])');
    await fileInput.setInputFiles(files.paths);
  }
  await page.getByRole("button", { name: "Оформить заказ" }).click();
  await page.waitForURL(/\/orders\/[^/]+$/);
  return page.url();
}

export function jobIdFromUrl(page: Page): string {
  const m = /\/orders\/([^/?#]+)/.exec(page.url());
  if (!m) {
    throw new Error(`no job id in ${page.url()}`);
  }
  return m[1];
}

/** Waits for the job status text to appear (SSE updates the page live). */
export async function waitForStatus(page: Page, text: string, timeout = 120_000) {
  await expect(page.getByText(text, { exact: false }).first()).toBeVisible({ timeout });
}

import { type APIRequestContext, request as playwrightRequest } from "@playwright/test";

/**
 * Opens an isolated API context signed in as the given user, so state can be
 * prepared as a client while the browser is signed in as the admin. The
 * context must be disposed by the caller.
 */
export async function apiContextAs(
  baseURL: string,
  userKey: "client" | "stranger" | "admin",
): Promise<APIRequestContext> {
  const user = testUsers[userKey];
  const context = await playwrightRequest.newContext({ baseURL });
  const resp = await context.post("/api/auth/login", {
    data: { email: user.email, password: user.password },
  });
  if (!resp.ok()) {
    throw new Error(`api login ${userKey}: ${resp.status()}`);
  }
  return context;
}

/** Creates an order for the given client API context and submits it. */
export async function createJobViaApi(
  context: APIRequestContext,
  prompt: string,
  file: { name: string; body: string | Uint8Array } = {
    name: "задание.txt",
    body: "исходные данные",
  },
): Promise<string> {
  const created = await context.post("/api/client/jobs", { data: { prompt } });
  const job = (await created.json()) as { id: string };
  const uploaded = await context.put(
    `/api/client/jobs/${job.id}/input?path=${encodeURIComponent(file.name)}`,
    {
      data: file.body,
    },
  );
  expect(uploaded.ok(), `upload input ${file.name}: HTTP ${uploaded.status()}`).toBe(true);
  await context.post(`/api/client/jobs/${job.id}/submit`);
  return job.id;
}

export interface AdminJobDetail {
  id: string;
  status: string;
  client_status: string;
  input_files?: { path: string; size: number; revision: number }[];
}

export interface JobStatus {
  client_status: string;
  current_version: number;
  question?: string | null;
  revisions?: { version: number; remarks: unknown[] }[];
}

/** Waits until the job reaches one of the given client_status values. */
export async function waitForApiStatus(
  page: Page,
  jobId: string,
  statuses: string | string[],
  timeout = 180_000,
): Promise<JobStatus> {
  const wanted = Array.isArray(statuses) ? statuses : [statuses];
  const deadline = Date.now() + timeout;
  for (;;) {
    const resp = await page.request.get(`/api/client/jobs/${jobId}`);
    if (resp.ok()) {
      const body = (await resp.json()) as JobStatus;
      if (wanted.includes(body.client_status)) {
        return body;
      }
    }
    if (Date.now() > deadline) {
      throw new Error(`job ${jobId} did not reach ${wanted.join("|")}`);
    }
    await page.waitForTimeout(500);
  }
}

/** Same status latch for a separate authenticated client API context. */
export async function waitForApiStatusInContext(
  context: APIRequestContext,
  jobId: string,
  statuses: string | string[],
  timeout = 180_000,
): Promise<JobStatus> {
  const wanted = Array.isArray(statuses) ? statuses : [statuses];
  let latest: JobStatus | undefined;
  await expect
    .poll(
      async () => {
        const response = await context.get(`/api/client/jobs/${jobId}`);
        if (!response.ok()) return `http-${response.status()}`;
        latest = (await response.json()) as JobStatus;
        return wanted.includes(latest.client_status) ? "reached" : latest.client_status;
      },
      { timeout, intervals: [100, 250, 500, 1000] },
    )
    .toBe("reached");
  if (!latest) throw new Error(`client API status for ${jobId} was not read`);
  return latest;
}
