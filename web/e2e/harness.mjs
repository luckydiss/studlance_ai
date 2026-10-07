import { spawn } from "node:child_process";
import { cp, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Playwright harness for PR 5 (09-tasks.md): boots the real Go server, the
// real worker and the fake agents (cmd/fakeagent) with a throwaway SQLite DB
// and blob dir. It creates its own users and worker token, starts/stops its
// own processes and never touches live services.

const here = path.dirname(fileURLToPath(import.meta.url));
export const repoRoot = path.resolve(here, "../..");

const isWindows = process.platform === "win32";
const exeSuffix = isWindows ? ".exe" : "";

export const testUsers = {
  client: { email: "client@example.com", password: "client-password-123", name: "Клиент Тестовый" },
  stranger: {
    email: "stranger@example.com",
    password: "stranger-password-3",
    name: "Другой Клиент",
  },
  admin: { email: "admin@example.com", password: "admin-password-123", name: "Админ Тестовый" },
};

function goBin() {
  return process.env.GO_BIN ?? "go";
}

function run(cmd, args, opts = {}) {
  return new Promise((resolve, reject) => {
    // pnpm is a .cmd shim on Windows: only it needs a shell (args with
    // spaces must not be concatenated into a shell line for the exes).
    const needsShell = opts.shell ?? (process.platform === "win32" && cmd === "pnpm");
    const child = spawn(cmd, args, {
      stdio: ["pipe", "pipe", "pipe"],
      shell: needsShell,
      ...opts,
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => {
      stdout += d.toString();
    });
    child.stderr.on("data", (d) => {
      stderr += d.toString();
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) {
        resolve({ stdout, stderr });
      } else {
        reject(new Error(`${cmd} ${args.join(" ")} exited ${code}\n${stdout}\n${stderr}`));
      }
    });
    if (opts.stdin) {
      child.stdin.write(opts.stdin);
      child.stdin.end();
    }
  });
}

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.unref();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

async function waitForHealth(baseUrl, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      const resp = await fetch(`${baseUrl}/healthz`);
      if (resp.ok) {
        return;
      }
    } catch {
      // not up yet
    }
    if (Date.now() > deadline) {
      throw new Error(`server did not become healthy at ${baseUrl}`);
    }
    await new Promise((r) => setTimeout(r, 250));
  }
}

function killTree(child) {
  if (!child || child.exitCode !== null) {
    return;
  }
  if (isWindows) {
    spawn("taskkill", ["/pid", String(child.pid), "/T", "/F"]);
  } else {
    try {
      child.kill("SIGKILL");
    } catch {
      // already gone
    }
  }
}

function findPdftoppm() {
  return process.env.PDFTOPPM ?? "pdftoppm";
}

export async function startHarness() {
  const tmp = await mkdtemp(path.join(tmpdir(), "studlance-e2e-"));
  const binDir = path.join(tmp, "bin");
  const dataDir = path.join(tmp, "data");
  const workDir = path.join(tmp, "work");
  await mkdir(binDir, { recursive: true });
  await mkdir(dataDir, { recursive: true });
  await mkdir(workDir, { recursive: true });
  // Demo images are intentionally absent: the harness checks the empty-demo
  // fallback (white sheets of the same size, no broken layout).
  await mkdir(path.join(dataDir, "demo"), { recursive: true });

  // The client SPA is embedded into the server binary (go:embed), so the web
  // build must run before compiling the server.
  if (!process.env.E2E_SKIP_WEB_BUILD) {
    await run(process.env.PNPM_BIN ?? "pnpm", ["-C", path.join(repoRoot, "web"), "build"]);
  }

  const serverBin = path.join(binDir, `studlance-server${exeSuffix}`);
  const workerBin = path.join(binDir, `studlance-worker${exeSuffix}`);
  const fakeagentBin = path.join(binDir, `fakeagent${exeSuffix}`);
  await run(goBin(), ["build", "-o", serverBin, "./cmd/server"], { cwd: repoRoot });
  await run(goBin(), ["build", "-o", workerBin, "./cmd/worker"], { cwd: repoRoot });
  await run(goBin(), ["build", "-o", fakeagentBin, "./cmd/fakeagent"], { cwd: repoRoot });
  // One binary copy per agent name: the mode is derived from argv[0], like
  // the real CLIs.
  const codexBin = path.join(binDir, `codex${exeSuffix}`);
  const claudeBin = path.join(binDir, `claude${exeSuffix}`);
  await cp(fakeagentBin, codexBin);
  await cp(fakeagentBin, claudeBin);

  for (const u of Object.values(testUsers)) {
    await run(
      serverBin,
      [
        "user",
        "create",
        "--email",
        u.email,
        "--role",
        u.email === testUsers.admin.email ? "admin" : "client",
        "--name",
        u.name,
        "--password-stdin",
        "--data",
        dataDir,
      ],
      { stdin: `${u.password}\n` },
    );
  }

  const tokenOut = await run(serverBin, ["worker", "token", "--name", "e2e-pw", "--data", dataDir]);
  const token = /token:\s*(\S+)/.exec(tokenOut.stdout)?.[1];
  if (!token) {
    throw new Error(`worker token not found in output: ${tokenOut.stdout}`);
  }

  const port = await freePort();
  const baseUrl = `http://127.0.0.1:${port}`;

  const server = spawn(serverBin, ["serve", "--addr", `127.0.0.1:${port}`, "--data", dataDir], {
    cwd: tmp,
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env },
  });
  server.stdout.on("data", (d) => process.env.E2E_VERBOSE && process.stdout.write(`[server] ${d}`));
  server.stderr.on("data", (d) => process.env.E2E_VERBOSE && process.stderr.write(`[server] ${d}`));
  await waitForHealth(baseUrl);

  const workerToml = path.join(tmp, "worker.toml");
  await writeFile(
    workerToml,
    [
      `server_url = ${JSON.stringify(baseUrl)}`,
      `token = ${JSON.stringify(token)}`,
      'name = "e2e-pw"',
      `work_dir = ${JSON.stringify(workDir)}`,
      `pdftoppm = ${JSON.stringify(findPdftoppm())}`,
      "",
      "[codex]",
      `command = ${JSON.stringify(codexBin)}`,
      "args = []",
      "",
      "[claude]",
      `command = ${JSON.stringify(claudeBin)}`,
      "args = []",
      "",
      "[timeouts]",
      'stage = "2m"',
      'heartbeat = "500ms"',
      "",
      "[capabilities]",
      "extra = []",
      "disable = []",
      "",
    ].join("\n"),
    "utf8",
  );

  const worker = spawn(workerBin, ["serve", "--config", workerToml], {
    cwd: tmp,
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env },
  });
  worker.stdout.on("data", (d) => process.env.E2E_VERBOSE && process.stdout.write(`[worker] ${d}`));
  worker.stderr.on("data", (d) => process.env.E2E_VERBOSE && process.stderr.write(`[worker] ${d}`));

  // An admin session cookie for test assertions (lease_epoch etc.) — the
  // login goes over HTTP like a browser.
  const adminLogin = await fetch(`${baseUrl}/api/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      email: testUsers.admin.email,
      password: testUsers.admin.password,
    }),
  });
  if (!adminLogin.ok) {
    throw new Error("harness: admin login failed");
  }
  const adminCookie =
    /sl_session=[^;]+/.exec(adminLogin.headers.get("set-cookie") ?? "")?.[0] ?? "";

  return {
    baseUrl,
    tmp,
    dataDir,
    server,
    worker,
    workerToken: token,
    adminCookie,
    async stop() {
      killTree(worker);
      killTree(server);
      await new Promise((r) => setTimeout(r, 500));
      await rm(tmp, { recursive: true, force: true }).catch(() => undefined);
    },
  };
}
