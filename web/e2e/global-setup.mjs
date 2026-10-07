import { startHarness } from "./harness.mjs";

// Global setup: boot the real server + worker + fake agents once for the
// whole run; the teardown stops both processes and removes the temp data.

export default async function globalSetup() {
  if (process.env.E2E_BASE_URL) {
    // External environment (CI or a manually started harness): nothing to do.
    return;
  }
  const harness = await startHarness();
  process.env.E2E_BASE_URL = harness.baseUrl;
  // In-memory only (never committed): let byte-exactness checks download the
  // uploaded input through the real worker API.
  process.env.E2E_WORKER_TOKEN = harness.workerToken;
  process.env.E2E_ADMIN_COOKIE = harness.adminCookie;
  return async () => {
    await harness.stop();
  };
}
