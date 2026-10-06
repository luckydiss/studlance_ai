import { type Page, expect } from "@playwright/test";
import { testUsers } from "../harness.mjs";

// Shared helpers for the PR 5 cabinet scenarios. All flows go through the
// real UI (09-tasks.md); the API is used only to prepare state.

export async function login(page: Page, userKey: "client" | "stranger" = "client") {
  const user = testUsers[userKey];
  await page.goto("/login");
  await page.getByLabel("Почта").fill(user.email);
  await page.getByLabel("Пароль").fill(user.password);
  await page.getByRole("button", { name: "Войти" }).click();
  await expect(page.getByRole("heading", { name: "Что нужно сделать?" })).toBeVisible();
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
