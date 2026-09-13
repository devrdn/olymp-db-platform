import { describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

// This handler's whole job is orchestration: ask the Core API for the story
// and for the contest's own title, then hand back a file. Both calls go
// through `serverRequest`, which itself calls `next/headers`'s `cookies()` —
// unavailable outside a real request (see app/(admin)/users/layout.test.tsx's
// own comment for the same reason). Faking the module boundary is what lets
// this run under vitest at all, and it is also the right boundary: what is
// under test here is what this route does with what the API answers, not how
// the session reaches the API.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

const { activeLocale } = vi.hoisted(() => ({ activeLocale: vi.fn() }));
vi.mock("@/lib/i18n/server", () => ({ activeLocale }));

import { GET } from "./route";

function ctx(contestId: string) {
  return { params: Promise.resolve({ contestId }) };
}

function contestListPayload(overrides: Partial<Record<string, unknown>> = {}) {
  return {
    items: [
      {
        id: "c1",
        status: "running",
        enrollment: "open",
        question_mode: "multi",
        lang: "en",
        title: "The Warehouse Fire",
        enrolled: true,
        scoring: "points",
        icpc_penalty_min: 20,
        ...overrides,
      },
    ],
    total: 1,
  };
}

describe("GET .../play/story.md", () => {
  test("serves the contest's title and the cleaned story as a Markdown file", async () => {
    activeLocale.mockResolvedValue("en");
    serverRequest.mockImplementation(async (path: string) => {
      if (path.startsWith("/contests/c1/play/story")) {
        // The literal `<br />` a WYSIWYG editor writes for an empty
        // paragraph (lib/format/markdown.ts's own doc): a story written
        // before that editor stopped storing them still carries one, and the
        // export must not hand it over verbatim.
        return { lang: "en", body_md: "A.\n\n<br />\n\nB." };
      }
      if (path.startsWith("/contests?")) {
        return contestListPayload();
      }
      throw new Error(`unexpected path ${path}`);
    });

    const response = await GET(new Request("http://x/contests/c1/play/story.md"), ctx("c1"));

    expect(response.status).toBe(200);
    expect(response.headers.get("Content-Type")).toBe("text/markdown; charset=utf-8");
    // The identifier, not the title: a title is authored text in any script
    // and this header is ASCII — the same reason the CSV and JSON exports on
    // the Go side name their files this way (participant_handler.go,
    // contest_package.go).
    expect(response.headers.get("Content-Disposition")).toBe('attachment; filename="story-c1.md"');
    const text = await response.text();
    expect(text).toBe("# The Warehouse Fire\n\nA.\n\nB.\n");
  });

  test("answers the API's own refusal rather than a generic failure", async () => {
    activeLocale.mockResolvedValue("en");
    serverRequest.mockRejectedValue(new ApiError("contest_finished", 409, "The participant has already finished"));

    const response = await GET(new Request("http://x/contests/c1/play/story.md"), ctx("c1"));

    expect(response.status).toBe(409);
    expect(await response.text()).toBe("The participant has already finished");
  });

  test("answers 404 when this contest has no story in this language", async () => {
    activeLocale.mockResolvedValue("en");
    serverRequest.mockRejectedValue(new ApiError("story_not_found", 404, "This contest has no story yet"));

    const response = await GET(new Request("http://x/contests/c1/play/story.md"), ctx("c1"));

    expect(response.status).toBe(404);
  });

  test("refuses rather than heading the file with the bare identifier when the caller is not on this contest's own roster", async () => {
    // The story read succeeded — this session is enrolled somewhere — but the
    // listing this route reads for the title does not carry *this* contest,
    // which can only mean the session is not actually taking part in it.
    activeLocale.mockResolvedValue("en");
    serverRequest.mockImplementation(async (path: string) => {
      if (path.startsWith("/contests/c1/play/story")) {
        return { lang: "en", body_md: "A story." };
      }
      if (path.startsWith("/contests?")) {
        return { items: [], total: 0 };
      }
      throw new Error(`unexpected path ${path}`);
    });

    const response = await GET(new Request("http://x/contests/c1/play/story.md"), ctx("c1"));

    expect(response.status).toBe(403);
  });

  test("fails rather than heading the file with a title it could not confirm", async () => {
    // The story read and the title read share one session and one
    // admission; a title lookup that fails while the story succeeds is not
    // "close enough" to degrade quietly — see this route's own doc.
    activeLocale.mockResolvedValue("en");
    serverRequest.mockImplementation(async (path: string) => {
      if (path.startsWith("/contests/c1/play/story")) {
        return { lang: "en", body_md: "A story." };
      }
      throw new ApiError("unreachable", 502, "Unparseable response from the API");
    });

    const response = await GET(new Request("http://x/contests/c1/play/story.md"), ctx("c1"));

    expect(response.status).toBe(502);
  });
});
