import { beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

// A Server Component: the API is faked, and the contest's wire format is
// `contests.test.ts`'s business.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/contests", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/contests")>()),
  contestSchema: { parse: (payload: unknown) => payload },
}));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import QuestionPage from "./page";

const CONTEST = "f767af3b-f135-40d2-a3a6-82d368de1004";
const QUESTION = "0b5c3c55-6a3e-4f43-9d6f-55b0e1f6a2d7";

beforeEach(() => {
  serverRequest.mockReset();
});

function page(questionId = QUESTION) {
  return QuestionPage({
    params: Promise.resolve({ contestId: CONTEST, questionId }),
  } as unknown as PageProps<"/contests/[contestId]/questions/[questionId]">);
}

const NOT_FOUND = { digest: expect.stringContaining("NEXT_HTTP_ERROR_FALLBACK;404") };

describe("a question's address", () => {
  test("is the not-found page for a question the contest does not have", async () => {
    serverRequest.mockImplementation(async (path: string) => {
      if (path === `/contests/${CONTEST}/questions/${QUESTION}`) {
        throw new ApiError("question_not_found", 404, "No such question in this contest");
      }
      if (path === `/contests/${CONTEST}/questions`) return { items: [] };
      return { id: CONTEST, status: "draft" };
    });

    await expect(page()).rejects.toMatchObject(NOT_FOUND);
  });

  test("is the not-found page for a segment that is not an id, before any request", async () => {
    await expect(page("not-an-id")).rejects.toMatchObject(NOT_FOUND);
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
