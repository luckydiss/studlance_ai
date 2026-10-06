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
  return async () => {
    await harness.stop();
  };
}
