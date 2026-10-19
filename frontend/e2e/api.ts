import { randomBytes } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";

import { request, type APIRequestContext } from "@playwright/test";

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

/** What the seed made, read by the specs and the teardown. */
export type Seed = {
  admin: Account;
  participant: Account;
  contest: { id: string; title: string };
  question: { id: string; answer: string; wrongAnswer: string };
};

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

export function writeSeed(seed: Seed): void {
  writePrivate(SEED_FILE, seed);
}

export function readSeed(): Seed {
  if (!existsSync(SEED_FILE)) {
    throw new Error(`no seed at ${SEED_FILE}: the global setup (e2e/seed.ts) did not run`);
  }
  return JSON.parse(readFileSync(SEED_FILE, "utf8")) as Seed;
}

/** Forgets the run's passwords once its accounts are closed. */
export function clearSeed(): void {
  rmSync(SEED_FILE, { force: true });
}

type SavedAdmin = { baseURL: string; login: string; password: string };

/**
 * The passwords to try for the stack's administrator, most recent first: the
 * one this suite set on an earlier run against the same stack, then the one
 * the operator gave. A one-time password works once, so the replacement is
 * kept for the next run.
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

export function saveAdmin(login: string, password: string): string {
  writePrivate(ADMIN_FILE, { baseURL: baseURL(), login, password } satisfies SavedAdmin);
  return ADMIN_FILE;
}

/** The stack's administrator, replacing a one-time password if it still has one. */
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

    // A freshly bootstrapped stack: the one-time password blocks everything
    // else, and the replacement is kept for the next run.
    const replacement = generatedPassword();
    await api.changePassword(password, replacement);
    const file = saveAdmin(login, replacement);
    console.log(`e2e: replaced ${login}'s one-time password; the new one is in ${file}`);
    if (await api.signIn(login, replacement)) {
      throw new Error(`${login} still has to change its password`);
    }
    return api;
  }
  throw new Error(`none of the passwords known for ${login} signs in`);
}
