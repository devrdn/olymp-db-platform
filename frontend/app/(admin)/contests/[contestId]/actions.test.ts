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

import { saveTranslationsAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

/** What the title form rendered at contest version 7 submits. */
function titleForm(): FormData {
  const data = new FormData();
  data.set("contestId", contestId);
  data.set("expectedVersion", "7");
  data.set("title.en", "Night in the archive");
  return data;
}

describe("saveTranslationsAction", () => {
  // A title moves the contest's version, and every other form on the page
  // sends the version back: it must read the new one.
  test("sends the version it was rendered at, then re-reads the contest's pages", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    expect(await saveTranslationsAction({}, titleForm())).toEqual({ ok: true });

    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/translations`, {
      method: "PUT",
      body: { translations: { en: { title: "Night in the archive" } }, expected_version: 7 },
      onHeaders: expect.any(Function),
    });
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
    expect(revalidatePath).toHaveBeenCalledWith("/contests");
  });

  test("refuses a form with no version without asking the server", async () => {
    const data = titleForm();
    data.delete("expectedVersion");

    expect(await saveTranslationsAction({}, data)).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("hands back the server's refusal of a contest that moved since", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("contest_changed", 409, "changed"));

    expect(await saveTranslationsAction({}, titleForm())).toEqual({ code: "contest_changed" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("never sends a request for a contest segment that is not an identifier", async () => {
    const data = new FormData();
    data.set("contestId", "../users");

    expect(await saveTranslationsAction({}, data)).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
