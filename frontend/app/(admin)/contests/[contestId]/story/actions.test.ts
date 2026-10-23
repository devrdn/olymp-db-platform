import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import { saveSchemaNotesAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";

function form(fields: Record<string, string>, id: string = contestId): FormData {
  const data = new FormData();
  data.set("contestId", id);
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

    expect(state).toEqual({ saved: true });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/schema-notes`, {
      method: "PUT",
      body: { translations: { en: "The suspects table." } },
    });
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}/story`);
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
      body: { translations: {} },
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
