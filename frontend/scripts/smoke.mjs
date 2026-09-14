// Serves the production build and asks it for a page, the way a person would.
//
// Every other check in this pipeline can be green while no page opens. Unit
// tests run under jsdom, which imports a "use client" module as plain
// JavaScript and never enforces the Server Component boundary; `next build`
// compiles every route but renders none of the dynamic ones. Both were green
// on the day every page answered 500 because a server layout called a function
// exported from a client module. Only a request to a running server sees that.
//
// So this starts the standalone server exactly as `npm run start` does, asks
// for /login — the page anybody reaches first, and one that renders without
// the API — and fails if the page is not a 200, if any script it references
// does not load, or if the server logged a render error on the way.
import { spawn } from "node:child_process";

const PORT = process.env.SMOKE_PORT ?? "3456";
const BASE = `http://127.0.0.1:${PORT}`;
const PATHS = ["/login"];
// Nothing listens here, on purpose: /login must render without the API, and a
// page that needs it has no business being the first one anybody sees.
const API_ORIGIN = "http://127.0.0.1:9";

// Refuse an occupied port. A server left over from an earlier run would answer
// in place of the one this check starts, and then the verdict describes a
// different build — which is exactly how this script once reported a failure
// for the wrong reason: its own previous server had survived and was answering.
try {
  await fetch(`${BASE}/login`, { redirect: "manual", signal: AbortSignal.timeout(1000) });
  console.error(`smoke: something is already listening on ${BASE}; stop it or set SMOKE_PORT`);
  process.exit(1);
} catch {
  // Nothing there, which is what this needs.
}

// Its own process group, so stopping it stops everything under it. serve.mjs
// starts next-server as a child; killing only serve.mjs left that child
// running, reparented, holding the port.
const server = spawn(process.execPath, ["scripts/serve.mjs"], {
  // No proxy in front, so nothing for INGRESS_SECRET to prove: the explicit
  // local flag lets the production entry point start without it.
  env: {
    ...process.env,
    PORT,
    API_ORIGIN,
    COOKIE_SECURE: "false",
    NODE_ENV: "production",
    ALLOW_MISSING_INGRESS_SECRET: "true",
  },
  stdio: ["ignore", "pipe", "pipe"],
  detached: true,
});

let log = "";
server.stdout.on("data", (chunk) => (log += chunk));
server.stderr.on("data", (chunk) => (log += chunk));

const failures = [];

function stop(code) {
  // The negative pid addresses the whole group, next-server included.
  try {
    process.kill(-server.pid, "SIGTERM");
  } catch {
    server.kill("SIGTERM");
  }
  if (failures.length) {
    console.error("\nsmoke: FAILED");
    for (const failure of failures) console.error(`  - ${failure}`);
    console.error("\n--- server log ---\n" + log);
  } else {
    console.log("smoke: ok");
  }
  process.exit(code);
}

async function waitForServer() {
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      await fetch(BASE + "/login", { redirect: "manual" });
      return true;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
  }
  return false;
}

try {
  if (!(await waitForServer())) {
    failures.push(`the server did not answer on ${BASE} within 30 seconds`);
    stop(1);
  }

  for (const path of PATHS) {
    const response = await fetch(BASE + path, { redirect: "manual" });
    const html = await response.text();
    if (response.status !== 200) {
      failures.push(`${path} answered ${response.status}, not 200`);
      continue;
    }

    const scripts = [...new Set(html.match(/\/_next\/static\/[^"]+\.js/g) ?? [])];
    if (scripts.length === 0) failures.push(`${path} references no scripts, so it cannot hydrate`);
    for (const script of scripts) {
      const asset = await fetch(BASE + script);
      if (asset.status !== 200) failures.push(`${path} references ${script}, which answered ${asset.status}`);
    }
    console.log(`smoke: ${path} 200, ${scripts.length} scripts`);
  }

  // Next prints a render failure with a leading cross even when it still
  // manages to send something; a 200 over a logged error is not a pass.
  const rendered = log.split("\n").filter((line) => line.includes("\u2a2f"));
  if (rendered.length) failures.push(`the server logged a render error: ${rendered[0].trim()}`);

  stop(failures.length ? 1 : 0);
} catch (error) {
  failures.push(`the smoke check itself failed: ${error}`);
  stop(1);
}
