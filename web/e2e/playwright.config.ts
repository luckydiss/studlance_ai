import { defineConfig } from "@playwright/test";

// PR 5 browser e2e (09-tasks.md): one real Go server + worker + fake agents
// per run (global setup), sequential scenarios on the client cabinet UI.
export default defineConfig({
  testDir: "./tests",
  timeout: 180_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://127.0.0.1:28080",
    screenshot: "only-on-failure",
    trace: "off",
  },
  outputDir: "./.artifacts/test-results",
  globalSetup: "./global-setup.mjs",
});
