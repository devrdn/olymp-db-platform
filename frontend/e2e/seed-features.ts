import { Api, generatedPassword, writeRecord, type Account, type SeedRecord } from "./api";

/**
 * The contests the post-beta checks (features-*.spec.ts) walk, made through
 * the public API by the global setup after the smoke test's own contest:
 *
 * - `features`: points, free order, read-only, catalogue open, downloads on,
 *   table descriptions, a titled question, a bonus question, prize places
 *   1/1/1, four tables joined by foreign keys. Alice, Bob, Carol and Dave are
 *   on it; the seed answers for Alice, Bob and Carol so the standings hold
 *   three different scores and Alice a penalty a bonus can give back.
 * - `sequential`: three questions in order; the second and third carry
 *   marker strings that must never reach Dave's browser.
 * - `hidden`: winner scoring, catalogue closed, downloads off, no table
 *   descriptions. Frank wins it in the seed; Erin plays it in the browser.
 * - `monitor`: a draft with a roster of ROSTER_SIZE imported accounts, for
 *   the monitoring screen. A draft keeps no spare databases, so the roster
 *   costs the game cluster nothing.
 */

export const ROSTER_SIZE = 300;

/** Strings that belong to questions a sequential participant has not reached. */
export const SECRET = {
  title2: "Ledger of the night porter",
  body2: "ZEBRA-WITHHELD-BODY-2",
  title3: "Which door was forced",
  body3: "ZEBRA-WITHHELD-BODY-3",
  choice3: "ZEBRA-WITHHELD-CHOICE",
} as const;

export type ContestRef = { id: string; title: string };
export type QuestionRef = { id: string; title: string; answer: string };

export type FeatureSeed = {
  accounts: Record<"alice" | "bob" | "carol" | "dave" | "erin" | "frank", Account>;
  contests: Partial<Record<"features" | "sequential" | "hidden" | "monitor", ContestRef>>;
  questions: Partial<Record<"titled" | "bonus" | "open" | "first" | "final", QuestionRef>>;
  /** The imported roster, deleted in bulk by the teardown. */
  roster: string[];
  notesHeading: string;
  complete: boolean;
};

/** A finished feature seed, which is all a spec reads: every contest and question made. */
export type CompleteFeatureSeed = Omit<FeatureSeed, "contests" | "questions"> & {
  contests: Required<FeatureSeed["contests"]>;
  questions: Required<FeatureSeed["questions"]>;
};

const FEATURES_SCRIPT = `
CREATE TABLE rooms (
    id   integer PRIMARY KEY,
    name text NOT NULL
);
CREATE TABLE suspects (
    id      integer PRIMARY KEY,
    name    text NOT NULL,
    alibi   text,
    room_id integer REFERENCES rooms (id)
);
CREATE TABLE sightings (
    id         integer PRIMARY KEY,
    suspect_id integer NOT NULL REFERENCES suspects (id),
    room_id    integer NOT NULL REFERENCES rooms (id),
    seen_at    time NOT NULL
);
CREATE TABLE "CaseFiles" (
    id   integer PRIMARY KEY,
    note text NOT NULL
);
INSERT INTO rooms (id, name) VALUES (1, 'Library'), (2, 'Kitchen'), (3, 'Study');
INSERT INTO suspects (id, name, alibi, room_id) VALUES
    (1, 'Alice Moreau', 'At the opera', 1),
    (2, 'Mallory Kent', NULL, 3),
    (3, 'Victor Hale', 'Asleep at home', 2),
    (4, 'Iris Vance', 'Walking the dog', 2),
    (5, 'Oscar Penn', 'At the pub', 1),
    (6, 'Nadia Frost', 'Working late', 3),
    (7, 'Hugo Lamb', 'On a train', NULL);
INSERT INTO sightings (id, suspect_id, room_id, seen_at) VALUES
    (1, 2, 3, '22:10'), (2, 3, 2, '21:40'), (3, 5, 1, '23:05');
INSERT INTO "CaseFiles" (id, note) VALUES (1, 'Window left open'), (2, 'Mud on the rug');
`;

const PLAIN_SCRIPT = `
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

const TITLED_BODY = [
  "The night porter swears every guest came back before midnight.",
  "",
  "| Room | Key holder |",
  "| --- | --- |",
  "| Library | the porter |",
  "| Study | nobody |",
  "",
  "Which suspect has **no alibi**?",
].join("\n");

const BUILD_TIMEOUT_MS = 240_000;
const DATABASE_TIMEOUT_MS = 240_000;

export async function seedFeatures(operator: Api, record: SeedRecord, run: string): Promise<void> {
  const accounts = {} as FeatureSeed["accounts"];
  const seed: FeatureSeed = {
    accounts,
    contests: {},
    questions: {},
    roster: [],
    notesHeading: "Reading the tables",
    complete: false,
  };
  record.features = seed;
  writeRecord(record);

  const people = [
    ["alice", "Alice Ashford"],
    ["bob", "Bob Brennan"],
    ["carol", "Carol Cho"],
    ["dave", "Dave Dunmore"],
    ["erin", "Erin Ellery"],
    ["frank", "Frank Fennimore"],
  ] as const;
  for (const [key, name] of people) {
    await createAccount(operator, accounts, key, `e2e-${key}-${run}`, name, () => writeRecord(record));
  }

  const features = await draftContest(operator, record, "features", `E2E features ${run}`, { scoring: "points" });
  const sequential = await draftContest(operator, record, "sequential", `E2E sequential ${run}`, {
    progression: "sequential",
  });
  const hidden = await draftContest(operator, record, "hidden", `E2E hidden ${run}`, {
    scoring: "winner",
    result_download: false,
  });
  const monitor = await draftContest(operator, record, "monitor", `E2E monitor ${run}`, {});

  await fillFeatures(operator, seed, features);
  await fillSequential(operator, seed, sequential);
  await fillHidden(operator, seed, hidden);
  writeRecord(record);

  // The three builds run side by side; the roster import overlaps them.
  const builds = Promise.all([features, sequential, hidden].map((c) => waitForBuild(operator, c.id)));
  await importRoster(operator, seed, monitor, run, () => writeRecord(record));
  await builds;

  await enrol(operator, features, [accounts.alice, accounts.bob, accounts.carol, accounts.dave]);
  await enrol(operator, sequential, [accounts.dave]);
  await enrol(operator, hidden, [accounts.erin, accounts.frank]);

  for (const contest of [features, sequential, hidden]) {
    await start(operator, contest);
  }
  await Promise.all([features, sequential, hidden].map((c) => waitForDatabase(operator, c.id)));

  await playScores(seed, features, hidden);

  seed.complete = true;
  writeRecord(record);
  console.log(`seed: the post-beta contests are running; the monitor roster holds ${seed.roster.length}`);
}

async function draftContest(
  operator: Api,
  record: SeedRecord,
  key: keyof FeatureSeed["contests"],
  title: string,
  extra: Record<string, unknown>,
): Promise<ContestRef> {
  const now = Date.now();
  const contest = await operator.call<{ id: string }>("POST", "/contests", {
    enrollment: "invite_only",
    question_mode: "multi",
    timing: "fixed",
    starts_at: new Date(now + 60 * 60_000).toISOString(),
    ends_at: new Date(now + 4 * 60 * 60_000).toISOString(),
    languages: [{ code: "en", is_default: true }],
    translations: { en: { title, description: "Created by the post-beta browser checks." } },
    ...extra,
  });
  const ref = { id: contest.id, title };
  record.features!.contests[key] = ref;
  writeRecord(record);
  return ref;
}

async function policy(operator: Api, contest: ContestRef, allowCatalog: boolean): Promise<void> {
  await operator.call("PUT", `/contests/${contest.id}/sql-policy`, {
    mode: "read_only",
    writable_tables: [],
    allow_create_view: false,
    allow_own_tables: false,
    allow_temp_tables: false,
    allow_catalog: allowCatalog,
    disk_quota_ratio: 5,
  });
}

async function question(
  operator: Api,
  contest: ContestRef,
  body: Record<string, unknown>,
  answers: { match_kind: string; value: string }[],
): Promise<string> {
  const created = await operator.call<{ id: string }>("POST", `/contests/${contest.id}/questions`, {
    is_visible: true,
    ...body,
  });
  await operator.call("PUT", `/contests/${contest.id}/questions/${created.id}/answers`, { answers });
  return created.id;
}

async function fillFeatures(operator: Api, seed: FeatureSeed, contest: ContestRef): Promise<void> {
  const at = `/contests/${contest.id}`;
  await operator.call("PUT", `${at}/story`, { translations: { en: "Four tables, one forced door." } });
  await operator.call("PUT", `${at}/schema-notes`, {
    translations: {
      en: `## ${seed.notesHeading}\n\n**suspects** lists everyone questioned; **sightings** says who was seen where.`,
    },
  });
  await policy(operator, contest, true);

  const titled = { title: "The alibi", answer: "Mallory Kent" };
  seed.questions.titled = {
    ...titled,
    id: await question(
      operator,
      contest,
      { kind: "text", points: 10, penalty_pct: 30, texts: { en: { title: titled.title, body_md: TITLED_BODY } } },
      [{ match_kind: "exact_ci", value: titled.answer }],
    ),
  };
  const bonus = { title: "The locked room", answer: "Study" };
  seed.questions.bonus = {
    ...bonus,
    id: await question(
      operator,
      contest,
      {
        kind: "text",
        points: 5,
        penalty_pct: 0,
        is_bonus: true,
        texts: { en: { title: bonus.title, body_md: "Which room was Mallory Kent in?" } },
      },
      [{ match_kind: "exact_ci", value: bonus.answer }],
    ),
  };
  const open = { title: "", answer: "Victor Hale" };
  seed.questions.open = {
    ...open,
    id: await question(
      operator,
      contest,
      { kind: "text", points: 10, penalty_pct: 0, texts: { en: { title: "", body_md: "Who was asleep at home?" } } },
      [{ match_kind: "exact_ci", value: open.answer }],
    ),
  };

  await operator.call("PUT", `${at}/prize-places`, { first: 1, second: 1, third: 1 });
  await operator.call("PUT", `${at}/game/script`, { script: FEATURES_SCRIPT });
}

async function fillSequential(operator: Api, seed: FeatureSeed, contest: ContestRef): Promise<void> {
  const at = `/contests/${contest.id}`;
  await operator.call("PUT", `${at}/story`, { translations: { en: "One lead at a time." } });
  await policy(operator, contest, true);
  const first = { title: "The first lead", answer: "Mallory Kent" };
  seed.questions.first = {
    ...first,
    id: await question(
      operator,
      contest,
      {
        kind: "text",
        points: 10,
        max_attempts: 3,
        texts: { en: { title: first.title, body_md: "Who has no alibi?" } },
      },
      [{ match_kind: "exact_ci", value: first.answer }],
    ),
  };
  await question(
    operator,
    contest,
    {
      kind: "text",
      points: 20,
      max_attempts: 3,
      texts: { en: { title: SECRET.title2, body_md: `Read ${SECRET.body2} and name the porter.` } },
    },
    [{ match_kind: "exact_ci", value: "the porter" }],
  );
  await question(
    operator,
    contest,
    {
      kind: "choice",
      points: 30,
      max_attempts: 1,
      choice_ids: ["front", "back"],
      texts: {
        en: {
          title: SECRET.title3,
          body_md: `Pick one. ${SECRET.body3}`,
          choices: { front: `${SECRET.choice3} front`, back: `${SECRET.choice3} back` },
        },
      },
    },
    [{ match_kind: "exact", value: "back" }],
  );
  await operator.call("PUT", `${at}/game/script`, { script: PLAIN_SCRIPT });
}

async function fillHidden(operator: Api, seed: FeatureSeed, contest: ContestRef): Promise<void> {
  const at = `/contests/${contest.id}`;
  await operator.call("PUT", `${at}/story`, { translations: { en: "Name the culprit first." } });
  await policy(operator, contest, false);
  await question(
    operator,
    contest,
    { kind: "text", points: 10, texts: { en: { title: "", body_md: "Who was at the opera?" } } },
    [{ match_kind: "exact_ci", value: "Alice Moreau" }],
  );
  const final = { title: "", answer: "Mallory Kent" };
  seed.questions.final = {
    ...final,
    id: await question(
      operator,
      contest,
      { kind: "final", points: 0, max_attempts: 3, texts: { en: { title: "", body_md: "Who did it?" } } },
      [{ match_kind: "exact_ci", value: final.answer }],
    ),
  };
  await operator.call("PUT", `${at}/game/script`, { script: PLAIN_SCRIPT });
}

/**
 * The standings the specs read: Bob 20 (first prize), Alice 7 with three
 * penalty points (second), Carol 4 (third), Dave nothing (no prize); Frank
 * wins the winner-mode contest.
 */
async function playScores(seed: FeatureSeed, features: ContestRef, hidden: ContestRef): Promise<void> {
  const { titled, open, final } = seed.questions;
  const answer = async (account: Account, contest: ContestRef, questionId: string, value: string) => {
    const api = await Api.open();
    try {
      await api.signIn(account.login, account.password);
      return await api.call<{ correct: boolean }>(
        "POST",
        `/contests/${contest.id}/questions/${questionId}/answer`,
        { value },
      );
    } finally {
      await api.close();
    }
  };
  const expect = (got: { correct: boolean }, correct: boolean, what: string) => {
    if (got.correct !== correct) throw new Error(`seed: ${what} was judged ${got.correct ? "correct" : "wrong"}`);
  };

  const { alice, bob, carol, frank } = seed.accounts;
  expect(await answer(bob, features, titled!.id, titled!.answer), true, "Bob's first answer");
  expect(await answer(bob, features, open!.id, open!.answer), true, "Bob's second answer");
  expect(await answer(alice, features, titled!.id, "Victor Hale"), false, "Alice's wrong answer");
  expect(await answer(alice, features, titled!.id, titled!.answer), true, "Alice's answer");
  expect(await answer(carol, features, titled!.id, "Iris Vance"), false, "Carol's first wrong answer");
  expect(await answer(carol, features, titled!.id, "Oscar Penn"), false, "Carol's second wrong answer");
  expect(await answer(carol, features, titled!.id, titled!.answer), true, "Carol's answer");
  expect(await answer(frank, hidden, final!.id, final!.answer), true, "Frank's verdict");
}

/**
 * Creates a student account under `key` and replaces its one-time password.
 * The account is in `accounts` and saved before anything else can fail, so a
 * failed sign-in still leaves it where the teardown looks.
 */
async function createAccount(
  operator: Api,
  accounts: FeatureSeed["accounts"],
  key: keyof FeatureSeed["accounts"],
  login: string,
  fullName: string,
  save: () => void,
): Promise<void> {
  const created = await operator.call<{ user: { id: string }; one_time_password: string }>("POST", "/users", {
    login,
    full_name: fullName,
    roles: ["student"],
  });
  accounts[key] = { id: created.user.id, login, password: "" };
  save();
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
  accounts[key] = { id: created.user.id, login, password };
  save();
}

/**
 * Imports the monitor roster in slices, retrying what a busy hashing pool
 * left out, and puts every account on the draft's roster.
 */
async function importRoster(
  operator: Api,
  seed: FeatureSeed,
  contest: ContestRef,
  run: string,
  save: () => void,
): Promise<void> {
  // The run's own tag: a deleted account keeps its login reserved, so a short
  // random one would in time collide on a long-lived stack.
  const tag = run;
  let pending = Array.from({ length: ROSTER_SIZE }, (_, i) => ({
    login: `e2e-r${tag}-${String(i + 1).padStart(3, "0")}`,
    full_name: `Roster ${String(i + 1).padStart(3, "0")} ${["Ash", "Birch", "Cedar", "Elm", "Fir"][i % 5]}`,
    email: "",
  }));
  const logins: string[] = [];
  for (let round = 0; pending.length > 0; round++) {
    if (round > 20) throw new Error(`seed: ${pending.length} roster accounts could not be imported`);
    const slice = pending.slice(0, 100);
    const result = await operator.call<{
      created: { user: { id: string; login: string } }[];
      skipped: { login: string; reason: string }[];
      not_imported: string[];
    }>("POST", "/users/import", { rows: slice, roles: ["student"], send_by_email: false });
    for (const one of result.created) {
      seed.roster.push(one.user.id);
      logins.push(one.user.login);
    }
    save();
    if (result.skipped.length > 0) {
      throw new Error(`seed: roster rows skipped: ${result.skipped.map((s) => s.reason).join(", ")}`);
    }
    const left = new Set(result.not_imported);
    pending = [...slice.filter((row) => left.has(row.login)), ...pending.slice(100)];
  }
  const added = await operator.call<{ added: number }>("POST", `/contests/${contest.id}/participants`, {
    logins,
    user_ids: [],
  });
  if (added.added !== logins.length) throw new Error(`seed: ${added.added} of ${logins.length} joined the roster`);
}

async function enrol(operator: Api, contest: ContestRef, people: Account[]): Promise<void> {
  const roster = await operator.call<{ added: number; skipped: { reason: string }[] }>(
    "POST",
    `/contests/${contest.id}/participants`,
    { logins: people.map((p) => p.login), user_ids: [] },
  );
  if (roster.added !== people.length) {
    throw new Error(`seed: ${contest.title} roster: ${roster.skipped.map((s) => s.reason).join(", ")}`);
  }
}

async function start(operator: Api, contest: ContestRef): Promise<void> {
  const at = `/contests/${contest.id}`;
  const check = await operator.call<{ ready: boolean; problems: { code: string }[] }>("GET", `${at}/publish-check`);
  if (!check.ready) {
    throw new Error(`seed: ${contest.title} cannot be published: ${check.problems.map((p) => p.code).join(", ")}`);
  }
  await operator.call("POST", `${at}/status`, { status: "published" });
  await operator.call("POST", `${at}/status`, { status: "running" });
}

async function waitForBuild(operator: Api, id: string): Promise<void> {
  const deadline = Date.now() + BUILD_TIMEOUT_MS;
  for (;;) {
    const game = await operator.call<{ status: string; build_error: string }>("GET", `/contests/${id}/game`);
    if (game.status === "ready") return;
    if (game.status === "failed") throw new Error(`seed: a game script failed to build: ${game.build_error}`);
    if (Date.now() > deadline) throw new Error(`seed: a game is still ${game.status} after four minutes`);
    await sleep(1_000);
  }
}

/** Every participant on the roster has a database, so no first query waits for one. */
async function waitForDatabase(operator: Api, id: string): Promise<void> {
  const deadline = Date.now() + DATABASE_TIMEOUT_MS;
  for (;;) {
    const list = await operator.call<{ instances: { status: string }[] }>("GET", `/contests/${id}/game/instances`);
    if (list.instances.filter((instance) => instance.status === "ready").length >= 3) return;
    if (Date.now() > deadline) throw new Error("seed: participant databases were not ready in four minutes");
    await sleep(1_000);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
