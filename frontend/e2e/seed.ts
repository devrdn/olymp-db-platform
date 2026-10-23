import { randomBytes } from "node:crypto";

import {
  Api,
  clearSeed,
  generatedPassword,
  readRecord,
  signInAsOperator,
  writeRecord,
  type Account,
  type SeedRecord,
} from "./api";
import { closeRecord } from "./teardown";

/**
 * The global setup: everything the smoke test needs, made through the public
 * API the way an organiser would make it, with names and passwords generated
 * for this run. It signs in as the stack's administrator (E2E_ADMIN_LOGIN,
 * E2E_ADMIN_PASSWORD), and from that account creates
 *
 * - an administrator of its own, so the browser never holds the operator's;
 * - a participant;
 * - a read_write contest with a story, one question and its answer, a small
 *   game script built into the template, and the participant on its roster;
 * - then publishes and starts it, and waits until a database is ready for
 *   the participant to claim.
 *
 * Each object goes into e2e/.state/seed.json (0600) the moment it exists,
 * for the specs and for the teardown, which Playwright runs even when this
 * setup throws. No password is printed.
 */

const GAME_SCRIPT = `
CREATE TABLE suspects (
    id    integer PRIMARY KEY,
    name  text NOT NULL,
    alibi text
);
INSERT INTO suspects (id, name, alibi) VALUES
    (1, 'Alice Moreau', 'At the opera'),
    (2, 'Mallory Kent', NULL),
    (3, 'Victor Hale', 'Asleep at home');
`;

const ANSWER = "Mallory Kent";
const WRONG_ANSWER = "Victor Hale";

const BUILD_TIMEOUT_MS = 180_000;
const DATABASE_TIMEOUT_MS = 180_000;

export default async function seed(): Promise<void> {
  const run = `${Date.now().toString(36)}${randomBytes(3).toString("hex")}`;
  const operator = await signInAsOperator();
  try {
    await closeLeftovers(operator);

    const record: SeedRecord = { complete: false };
    writeRecord(record);

    await createAccount(operator, record, "admin", `e2e-admin-${run}`, "E2E Administrator", ["admin"]);
    const participant = await createAccount(
      operator,
      record,
      "participant",
      `e2e-player-${run}`,
      "E2E Participant",
      ["student"],
    );
    const title = `E2E smoke ${run}`;

    const now = Date.now();
    // Started by hand below; the schedule only has to exist and lie ahead,
    // so the scheduler never moves the contest on its own during the run.
    const contest = await operator.call<{ id: string }>("POST", "/contests", {
      enrollment: "invite_only",
      question_mode: "multi",
      timing: "fixed",
      starts_at: new Date(now + 60 * 60_000).toISOString(),
      ends_at: new Date(now + 4 * 60 * 60_000).toISOString(),
      languages: [{ code: "en", is_default: true }],
      translations: { en: { title, description: "Created by the browser smoke test." } },
    });
    record.contest = { id: contest.id, title };
    writeRecord(record);
    const at = `/contests/${contest.id}`;

    await operator.call("PUT", `${at}/story`, {
      translations: { en: "Three suspects, one without an alibi." },
    });
    await operator.call("PUT", `${at}/sql-policy`, {
      mode: "read_write",
      writable_tables: ["suspects"],
      allow_create_view: false,
      allow_own_tables: false,
      allow_temp_tables: false,
      allow_catalog: false,
      disk_quota_ratio: 5,
    });
    const question = await operator.call<{ id: string }>("POST", `${at}/questions`, {
      kind: "text",
      points: 10,
      is_visible: true,
      texts: { en: { title: "The alibi", body_md: "Which suspect has no alibi?" } },
    });
    record.question = { id: question.id, answer: ANSWER, wrongAnswer: WRONG_ANSWER };
    writeRecord(record);
    await operator.call("PUT", `${at}/questions/${question.id}/answers`, {
      answers: [{ match_kind: "exact_ci", value: ANSWER }],
    });

    await operator.call("PUT", `${at}/game/script`, { script: GAME_SCRIPT });
    await waitForBuild(operator, at);

    const roster = await operator.call<{ added: number; skipped: { reason: string }[] }>(
      "POST",
      `${at}/participants`,
      { logins: [participant.login], user_ids: [] },
    );
    if (roster.added !== 1) {
      throw new Error(`seed: the participant was not added: ${roster.skipped.map((s) => s.reason).join(", ")}`);
    }

    const check = await operator.call<{ ready: boolean; problems: { code: string }[] }>("GET", `${at}/publish-check`);
    if (!check.ready) {
      throw new Error(`seed: the contest cannot be published: ${check.problems.map((p) => p.code).join(", ")}`);
    }
    await operator.call("POST", `${at}/status`, { status: "published" });
    await operator.call("POST", `${at}/status`, { status: "running" });
    await waitForDatabase(operator, at);

    record.complete = true;
    writeRecord(record);
    console.log(`seed: contest "${title}" is running with one participant`);
  } finally {
    await operator.close();
  }
}

/**
 * Closes what an earlier run recorded and its teardown could not, before this
 * run's record replaces the only list of it. Refuses to go on if that fails.
 */
async function closeLeftovers(operator: Api): Promise<void> {
  const leftover = readRecord();
  if (!leftover) return;
  const failures = await closeRecord(operator, leftover);
  if (failures.length > 0) {
    throw new Error(`seed: an earlier run left objects that could not be closed (${failures.join("; ")}); see seed.json`);
  }
  clearSeed();
  console.log("seed: closed what an earlier run left behind");
}

/**
 * Creates an account and replaces its one-time password, as its owner would at
 * first sign-in. It is recorded under `key` as soon as it exists, and again
 * with its password once that is set.
 */
async function createAccount(
  operator: Api,
  record: SeedRecord,
  key: "admin" | "participant",
  login: string,
  fullName: string,
  roles: string[],
): Promise<Account> {
  const created = await operator.call<{ user: { id: string }; one_time_password: string }>("POST", "/users", {
    login,
    full_name: fullName,
    roles,
  });
  record[key] = { id: created.user.id, login, password: "" };
  writeRecord(record);

  const password = generatedPassword();
  const owner = await Api.open();
  try {
    if (!(await owner.signIn(login, created.one_time_password))) {
      throw new Error(`seed: ${login} was created without a one-time password`);
    }
    await owner.changePassword(created.one_time_password, password);
  } finally {
    await owner.close();
  }
  const account = { id: created.user.id, login, password };
  record[key] = account;
  writeRecord(record);
  return account;
}

async function waitForBuild(operator: Api, at: string): Promise<void> {
  const deadline = Date.now() + BUILD_TIMEOUT_MS;
  for (;;) {
    const game = await operator.call<{ status: string; build_error: string }>("GET", `${at}/game`);
    if (game.status === "ready") return;
    if (game.status === "failed") throw new Error(`seed: the game script failed to build: ${game.build_error}`);
    if (Date.now() > deadline) throw new Error(`seed: the game is still ${game.status} after three minutes`);
    await sleep(1_000);
  }
}

/**
 * Waits for one ready copy of the game. The participant's first query claims
 * it; without one, that query would wait for a copy to be made, or be told
 * no_game_yet.
 */
async function waitForDatabase(operator: Api, at: string): Promise<void> {
  const deadline = Date.now() + DATABASE_TIMEOUT_MS;
  for (;;) {
    const list = await operator.call<{ instances: { status: string }[] }>("GET", `${at}/game/instances`);
    if (list.instances.some((instance) => instance.status === "ready")) return;
    if (Date.now() > deadline) throw new Error("seed: no participant database became ready in three minutes");
    await sleep(1_000);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
