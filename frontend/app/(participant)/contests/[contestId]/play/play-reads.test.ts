import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

// The API is faked, and the wire formats are the schema tests' business, so
// each answer passes through as it was sent.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/play", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/play")>()),
  playStorySchema: { parse: (payload: unknown) => payload },
  playQuestionListSchema: { parse: (payload: unknown) => payload },
}));
vi.mock("@/lib/api/querylog", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/querylog")>()),
  queryLogResponseSchema: { parse: (payload: unknown) => payload },
}));
vi.mock("@/lib/api/schema", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/schema")>()),
  gameSchemaSchema: { parse: (payload: unknown) => payload },
}));
vi.mock("@/lib/api/workspace", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/workspace")>()),
  workspaceSchema: { parse: (payload: unknown) => payload },
}));

import { readPlay } from "./play-reads";

const CONTEST = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";
const base = `/contests/${CONTEST}/play`;

const answers: Record<string, unknown> = {
  story: { bodyMd: "Once upon a table.", schemaNotesMd: null },
  questions: { items: [{ id: "q1" }] },
  log: { items: [], total: 0 },
  schema: { tables: [] },
  workspace: { notes: "" },
};

/** The five reads' answers, with each named one refused with its error. */
function serve(refused: Record<string, unknown> = {}) {
  serverRequest.mockImplementation(async (path: string) => {
    const read = path.slice(base.length + 1).split("?")[0];
    if (read in refused) throw refused[read];
    if (read in answers) return answers[read];
    throw new Error(`unexpected ${path}`);
  });
}

beforeEach(() => {
  serverRequest.mockReset();
  vi.spyOn(console, "warn").mockImplementation(() => {});
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  vi.restoreAllMocks();
});

const signIn = { digest: expect.stringContaining(`/login?next=${encodeURIComponent(base)}`) };

describe("the play screen's reads", () => {
  test("give the workspace what it is built from", async () => {
    serve();

    expect(await readPlay(CONTEST, "en")).toEqual({
      kind: "ready",
      questions: [{ id: "q1" }],
      story: answers.story,
      log: { items: [], total: 0, failed: false },
      schema: answers.schema,
      workspace: answers.workspace,
    });
  });

  test.each(["story", "questions", "log", "schema", "workspace"])(
    "send a session that died after the contest listing to sign-in, whichever read (%s) meets it",
    async (read) => {
      serve({ [read]: new ApiError("unauthenticated", 401, "Sign in") });

      await expect(readPlay(CONTEST, "en")).rejects.toMatchObject(signIn);
    },
  );

  test.each(["story", "questions", "log"])("take the screen on a refusal of the admission gate, from %s", async (read) => {
    serve({ [read]: new ApiError("deadline_passed", 403, "") });

    expect(await readPlay(CONTEST, "en")).toEqual({ kind: "unavailable", code: "deadline_passed" });
  });

  test("answer the questions' refusal before the story's", async () => {
    serve({
      questions: new ApiError("address_not_allowed", 403, ""),
      story: new ApiError("internal_error", 500, ""),
    });

    expect(await readPlay(CONTEST, "en")).toEqual({ kind: "unavailable", code: "address_not_allowed" });
  });

  test("keep the screen without a story that is not written", async () => {
    serve({ story: new ApiError("story_not_found", 404, "") });

    expect(await readPlay(CONTEST, "en")).toMatchObject({ kind: "ready", story: null });
  });

  test("send any other refusal of the questions or the story to the error boundary", async () => {
    serve({ questions: new ApiError("internal_error", 500, "") });
    await expect(readPlay(CONTEST, "en")).rejects.toMatchObject({ code: "internal_error" });

    serve({ story: new ApiError("internal_error", 500, "") });
    await expect(readPlay(CONTEST, "en")).rejects.toMatchObject({ code: "internal_error" });
  });

  test("keep the screen without the log, the schema or the workspace", async () => {
    serve({
      log: new ApiError("internal_error", 500, ""),
      schema: new ApiError("schema_hidden", 403, ""),
      workspace: new ApiError("workspace_too_often", 429, ""),
    });

    expect(await readPlay(CONTEST, "en")).toMatchObject({
      kind: "ready",
      log: { items: [], total: 0, failed: true },
      schema: null,
      workspace: null,
    });
  });

  test("log a schema the contest hides, or the gate's refusal, as nothing worth a line", async () => {
    serve({ schema: new ApiError("schema_hidden", 403, ""), workspace: new ApiError("not_a_participant", 403, "") });

    await readPlay(CONTEST, "en");

    expect(console.error).not.toHaveBeenCalled();
  });
});
