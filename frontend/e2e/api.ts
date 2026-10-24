import { execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";

import { request, type APIRequestContext } from "@playwright/test";

import type { FeatureSeed } from "./seed-features";

/**
 * The Core API as the seed and the teardown use it: through the stack's own
 * public address, the way a browser reaches it, never the database. Every
 * failure names the method, the path, the status and the API's error code,
 * and never a request or response body, which can carry a password.
 */

/** Where a run keeps what it generated. Git-ignored; written 0600. */
export const STATE_DIR = process.env.E2E_STATE_DIR ?? path.resolve(__dirname, ".state");
const SEED_FILE = path.join(STATE_DIR, "seed.json");
const ADMIN_FILE = path.join(STATE_DIR, "admin.json");

export type Account = { id: string; login: string; password: string };

/**
 * What the seed has made so far. Each object is recorded the moment the API
 * creates it, so a setup that fails halfway still tells the teardown what to
 * close; an account's password is empty until its one-time one is replaced.
 */
export type SeedRecord = {
  complete: boolean;
  admin?: Account;
  participant?: Account;
  contest?: { id: string; title: string };
  question?: { id: string; answer: string; wrongAnswer: string };
  /** The post-beta checks' accounts and contests (seed-features.ts). */
  features?: FeatureSeed;
};

/** A finished seed, which is all a spec ever reads. */
export type Seed = Required<SeedRecord>;

export function baseURL(): string {
  const url = process.env.E2E_BASE_URL;
  if (!url) {
    throw new Error("E2E_BASE_URL is not set: name the stack to test, e.g. https://localhost:18443");
  }
  return url.replace(/\/+$/, "");
}

/** Set for a stack whose certificate the browser cannot verify, such as Caddy's local CA. */
export function ignoreHTTPSErrors(): boolean {
  return process.env.E2E_IGNORE_HTTPS_ERRORS === "1";
}

/** A password nobody chose: 24 characters, twice the API's minimum. */
export function generatedPassword(): string {
  return randomBytes(18).toString("base64url");
}

export class ApiError extends Error {
  constructor(
    readonly method: string,
    readonly route: string,
    readonly status: number,
    readonly code: string,
  ) {
    super(`${method} ${route} answered ${status}${code ? ` (${code})` : ""}`);
  }
}

/** One signed-in (or not yet signed-in) client of the API, with its own cookies. */
export class Api {
  private constructor(private readonly context: APIRequestContext) {}

  static async open(): Promise<Api> {
    const context = await request.newContext({
      baseURL: baseURL(),
      ignoreHTTPSErrors: ignoreHTTPSErrors(),
      extraHTTPHeaders: { "Accept-Language": "en" },
    });
    return new Api(context);
  }

  async call<T = unknown>(method: string, route: string, data?: unknown): Promise<T> {
    const response = await this.context.fetch(`/api/v1${route}`, {
      method,
      data,
      failOnStatusCode: false,
      maxRedirects: 0,
    });
    if (!response.ok()) {
      let code = "";
      try {
        const body = (await response.json()) as { error?: { code?: string } };
        code = body.error?.code ?? "";
      } catch {
        // Not JSON: the status says enough, and the body is not repeated.
      }
      throw new ApiError(method, route, response.status(), code);
    }
    if (response.status() === 204) return undefined as T;
    return (await response.json()) as T;
  }

  /** Signs in; true when the account must replace a one-time password first. */
  async signIn(login: string, password: string): Promise<boolean> {
    const body = await this.call<{ must_change_password: boolean }>("POST", "/auth/login", { login, password });
    return body.must_change_password;
  }

  /** Replaces the password. The API retires every session of the account, this one included. */
  async changePassword(current: string, next: string): Promise<void> {
    await this.call("POST", "/auth/password", { old_password: current, new_password: next });
  }

  async close(): Promise<void> {
    await this.context.dispose();
  }
}

function writePrivate(file: string, value: unknown): void {
  mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  writeFileSync(file, JSON.stringify(value, null, 2), { mode: 0o600 });
  chmodSync(file, 0o600);
}

export function writeRecord(record: SeedRecord): void {
  writePrivate(SEED_FILE, record);
}

/** What an earlier setup recorded, or null when there is nothing to close. */
export function readRecord(): SeedRecord | null {
  if (!existsSync(SEED_FILE)) return null;
  return JSON.parse(readFileSync(SEED_FILE, "utf8")) as SeedRecord;
}

export function readSeed(): Seed {
  const record = readRecord();
  if (!record?.complete) {
    throw new Error(`no finished seed at ${SEED_FILE}: the global setup (e2e/seed.ts) did not complete`);
  }
  return record as Seed;
}

/** Forgets the run's passwords once its accounts are closed. */
export function clearSeed(): void {
  rmSync(SEED_FILE, { force: true });
}

type SavedAdmin = { baseURL: string; login: string; password: string };

/**
 * The passwords to try for the stack's administrator, most recent first: the
 * one this suite set on an earlier run against the same stack, then the one
 * the operator gave.
 */
export function adminCandidates(login: string): string[] {
  const candidates: string[] = [];
  if (existsSync(ADMIN_FILE)) {
    const saved = JSON.parse(readFileSync(ADMIN_FILE, "utf8")) as SavedAdmin;
    if (saved.baseURL === baseURL() && saved.login === login) candidates.push(saved.password);
  }
  const given = process.env.E2E_ADMIN_PASSWORD;
  if (given && !candidates.includes(given)) candidates.push(given);
  return candidates;
}

/**
 * Refuses a file a commit could pick up: inside a git work tree it must be
 * ignored. Outside one (an E2E_STATE_DIR elsewhere) there is nothing to commit.
 */
function assertNotCommittable(file: string): void {
  let dir = path.dirname(file);
  while (!existsSync(dir)) dir = path.dirname(dir);
  try {
    execFileSync("git", ["-C", dir, "rev-parse", "--is-inside-work-tree"], { stdio: "ignore" });
  } catch {
    return;
  }
  try {
    execFileSync("git", ["-C", dir, "check-ignore", "-q", file], { stdio: "ignore" });
  } catch {
    throw new Error(`refusing to write a password to ${file}: it is inside a git work tree and not ignored`);
  }
}

/**
 * Writes the administrator's next password and proves it can be read back,
 * before the API is asked to change it: a password that exists only in a
 * failed write is an administrator nobody can sign in as. Returns a function
 * that puts the file back as it was, for a change the API refuses.
 */
function saveAdminBeforeChange(login: string, password: string): () => void {
  assertNotCommittable(ADMIN_FILE);
  const previous = existsSync(ADMIN_FILE) ? readFileSync(ADMIN_FILE, "utf8") : null;
  writePrivate(ADMIN_FILE, { baseURL: baseURL(), login, password } satisfies SavedAdmin);

  const back = JSON.parse(readFileSync(ADMIN_FILE, "utf8")) as SavedAdmin;
  if (back.password !== password || back.login !== login || back.baseURL !== baseURL()) {
    throw new Error(`${ADMIN_FILE} does not read back as written; the password was not changed`);
  }
  if ((statSync(ADMIN_FILE).mode & 0o077) !== 0) {
    throw new Error(`${ADMIN_FILE} is readable by others; the password was not changed`);
  }

  return () => {
    if (previous === null) rmSync(ADMIN_FILE, { force: true });
    else writeFileSync(ADMIN_FILE, previous, { mode: 0o600 });
  };
}

/**
 * Set only for a stack this suite may take over: e2e/stack.sh writes it into
 * the smoke.env of the throwaway stack it started.
 */
const ALLOW_PASSWORD_CHANGE = "E2E_ALLOW_ADMIN_PASSWORD_CHANGE";

/**
 * The stack's administrator. An account still on a one-time password is
 * refused unless ALLOW_PASSWORD_CHANGE is "1": its owner expects to choose
 * that password, and on a real installation a password only this checkout
 * knows is an administrator locked out.
 */
export async function signInAsOperator(): Promise<Api> {
  const login = process.env.E2E_ADMIN_LOGIN ?? "admin";
  const candidates = adminCandidates(login);
  if (candidates.length === 0) {
    throw new Error("set E2E_ADMIN_PASSWORD to the password of the administrator E2E_ADMIN_LOGIN");
  }

  for (const password of candidates) {
    const api = await Api.open();
    let mustChange: boolean;
    try {
      mustChange = await api.signIn(login, password);
    } catch (error) {
      await api.close();
      if (error instanceof ApiError && error.status === 401) continue;
      throw error;
    }
    if (!mustChange) return api;

    if (process.env[ALLOW_PASSWORD_CHANGE] !== "1") {
      await api.close();
      throw new Error(
        `${login} still has a one-time password. Sign in once by hand and set one, then give that ` +
          `as E2E_ADMIN_PASSWORD; this suite replaces it only on a stack it started (${ALLOW_PASSWORD_CHANGE}=1).`,
      );
    }

    const replacement = generatedPassword();
    const restore = saveAdminBeforeChange(login, replacement);
    try {
      await api.changePassword(password, replacement);
    } catch (error) {
      restore();
      await api.close();
      throw error;
    }
    console.log(`e2e: replaced ${login}'s one-time password; the new one is in ${ADMIN_FILE}`);
    if (await api.signIn(login, replacement)) {
      throw new Error(`${login} still has to change its password`);
    }
    return api;
  }
  throw new Error(`none of the passwords known for ${login} signs in`);
}
