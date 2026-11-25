import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/server")>()),
  serverRequest,
}));

import { ApiError } from "@/lib/api/client";

import { saveSchemaNotesAction, saveStoryAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";

/** What a form rendered at contest version 7 submits. */
function form(fields: Record<string, string>, id: string = contestId): FormData {
  const data = new FormData();
  data.set("contestId", id);
  data.set("expectedVersion", "7");
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("saveSchemaNotesAction", () => {
  test("replaces the whole set in one request, leaving out the boxes left empty", async () => {
    serverRequest.mockResolvedValueOnce({ translations: {} });

    const state = await saveSchemaNotesAction(
      {},
      form({ "schemaNotes.en": "  The suspects table.  ", "schemaNotes.ro": "   ", "body.en": "not ours" }),
    );

    expect(state).toEqual({ ok: true });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/schema-notes`, {
      method: "PUT",
      body: { translations: { en: "The suspects table." }, expected_version: 7 },
      onHeaders: expect.any(Function),
    });
    // The save moves the contest's version, which the settings form sends back.
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("cleans the editor's literal line breaks before storing", async () => {
    serverRequest.mockResolvedValueOnce({ translations: {} });

    await saveSchemaNotesAction({}, form({ "schemaNotes.en": "One\n\n<br />\n\nTwo" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: { translations: Record<string, string> } }];
    expect(init.body.translations.en).not.toContain("<br />");
  });

  test("sends an empty set when every box is empty, which removes the tab", async () => {
    serverRequest.mockResolvedValueOnce({ translations: {} });

    await saveSchemaNotesAction({}, form({ "schemaNotes.en": "" }));

    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/schema-notes`, {
      method: "PUT",
      body: { translations: {}, expected_version: 7 },
      onHeaders: expect.any(Function),
    });
  });

  test("refuses a contest id that is not one, without a request", async () => {
    const state = await saveSchemaNotesAction({}, form({ "schemaNotes.en": "x" }, "../users"));

    expect(state).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("reports the API's refusal by its code and revalidates nothing", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("not_editable", 409, "frozen"));

    const state = await saveSchemaNotesAction({}, form({ "schemaNotes.en": "x" }));

    expect(state).toEqual({ code: "not_editable" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});

describe("saveStoryAction", () => {
  test("replaces the story in one request, leaving out the boxes left empty", async () => {
    serverRequest.mockResolvedValueOnce({});

    const state = await saveStoryAction(
      {},
      form({ "body.en": "  A body in the stacks.  ", "body.ro": "  ", "schemaNotes.en": "not ours" }),
    );

    expect(state).toEqual({ ok: true });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/story`, {
      method: "PUT",
      body: { translations: { en: "A body in the stacks." }, expected_version: 7 },
      onHeaders: expect.any(Function),
    });
    // The publish gate counts the story, so the whole contest layout refreshes.
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("cleans the editor's literal line breaks before storing", async () => {
    serverRequest.mockResolvedValueOnce({});

    await saveStoryAction({}, form({ "body.en": "One\n\n<br />\n\nTwo" }));

    const [, init] = serverRequest.mock.calls[0] as [string, { body: { translations: Record<string, string> } }];
    expect(init.body.translations.en).not.toContain("<br />");
  });

  test("refuses a contest id that is not one, without a request", async () => {
    expect(await saveStoryAction({}, form({ "body.en": "x" }, "../users"))).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("reports the API's refusal by its code and revalidates nothing", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("not_editable", 409, "frozen"));

    expect(await saveStoryAction({}, form({ "body.en": "x" }))).toEqual({ code: "not_editable" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});

describe.each([
  ["saveStoryAction", saveStoryAction, "body"],
  ["saveSchemaNotesAction", saveSchemaNotesAction, "schemaNotes"],
] as const)("%s, the version the form was rendered at", (_name, action, prefix) => {
  test("refuses a form with no version without asking the server", async () => {
    const data = form({ [`${prefix}.en`]: "x" });
    data.delete("expectedVersion");

    expect(await action({}, data)).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("hands back the server's refusal of a contest that moved since", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("contest_changed", 409, "changed"));

    expect(await action({}, form({ [`${prefix}.en`]: "x" }))).toEqual({ code: "contest_changed" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});
