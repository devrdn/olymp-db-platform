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

import { saveTranslationsAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("saveTranslationsAction", () => {
  // A title moves the contest's version, and the settings form below the
  // heading sends the version back: it must read the new one.
  test("re-reads the contest's pages, the settings form's version among them", async () => {
    serverRequest.mockResolvedValueOnce(undefined);
    const data = new FormData();
    data.set("contestId", contestId);
    data.set("title.en", "Night in the archive");

    expect(await saveTranslationsAction({}, data)).toEqual({ ok: true });

    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/translations`, {
      method: "PUT",
      body: { translations: { en: { title: "Night in the archive" } } },
    });
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("never sends a request for a contest segment that is not an identifier", async () => {
    const data = new FormData();
    data.set("contestId", "../users");

    expect(await saveTranslationsAction({}, data)).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
