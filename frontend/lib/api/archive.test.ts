import { describe, expect, test } from "vitest";

import {
  ARCHIVE_MAX_PAGE,
  ARCHIVE_PAGE_SIZE,
  ARCHIVE_QUERY_MAX,
  archiveContestSchema,
  archivePageSchema,
  archiveSearch,
} from "./archive";

describe("archiveSearch", () => {
  test("trims the search and turns the page into an offset", () => {
    expect(archiveSearch(" sql ", 3, "en").toString()).toBe("q=sql&limit=20&offset=40&lang=en");
  });

  test("drops an empty search and starts at the first page", () => {
    const search = archiveSearch("   ", 0, "ro");
    expect(search.has("q")).toBe(false);
    expect(search.get("offset")).toBe("0");
    expect(search.get("lang")).toBe("ro");
  });

  test("a page that is not a whole number from 1 up is the first", () => {
    expect(archiveSearch(undefined, 1.5, "en").get("offset")).toBe("0");
    expect(archiveSearch(undefined, -2, "en").get("offset")).toBe("0");
    expect(archiveSearch(undefined, Number.NaN, "en").get("offset")).toBe("0");
    expect(archiveSearch(undefined, undefined, "en").get("offset")).toBe("0");
  });

  test("a page past the safe integers is the first, and a huge one is capped", () => {
    expect(archiveSearch(undefined, 2 ** 53, "en").get("offset")).toBe("0");
    expect(archiveSearch(undefined, Number.MAX_SAFE_INTEGER, "en").get("offset")).toBe(
      String((ARCHIVE_MAX_PAGE - 1) * ARCHIVE_PAGE_SIZE),
    );
    expect(Number.isSafeInteger((ARCHIVE_MAX_PAGE - 1) * ARCHIVE_PAGE_SIZE)).toBe(true);
  });

  test("cuts a long search to the length the API accepts", () => {
    const search = archiveSearch("a".repeat(150), 1, "en");
    expect(search.get("q")).toHaveLength(ARCHIVE_QUERY_MAX);
    expect(ARCHIVE_QUERY_MAX).toBe(100);
    expect(ARCHIVE_PAGE_SIZE).toBe(20);
  });

  test("counts characters, so a cut never splits one", () => {
    const search = archiveSearch("😀".repeat(150), 1, "en");
    expect([...(search.get("q") ?? "")]).toHaveLength(ARCHIVE_QUERY_MAX);
  });
});

const item = { id: "i", title: "T", status: "archived", participants: 3 };

describe("archivePageSchema", () => {
  test("reads a page and drops what it does not know", () => {
    const page = archivePageSchema.parse({ items: [{ ...item, secret: 1 }], total: 1 });
    expect(page.items[0]).toEqual({ ...item });
  });

  test("refuses more rows than the API ever sends", () => {
    const items = Array.from({ length: 51 }, () => item);
    expect(archivePageSchema.safeParse({ items, total: 51 }).success).toBe(false);
  });
});

const question = {
  position: 1,
  kind: "choice",
  points: 2,
  is_bonus: false,
  title: "Q",
  body_md: "Body",
  choice_ids: ["a"],
  choices: { a: "Alpha" },
};

describe("archiveContestSchema", () => {
  const base = { id: "c", title: "T", description: "", status: "archived", participants: 0 };

  test("a contest without tasks has none", () => {
    expect(archiveContestSchema.parse(base).tasks).toBeUndefined();
  });

  test("drops an unknown field such as a reference answer from a question", () => {
    const parsed = archiveContestSchema.parse({
      ...base,
      tasks: { lang: "en", story_md: "S", schema_notes_md: "", questions: [{ ...question, reference: "SELECT 1" }] },
    });
    const read = parsed.tasks?.questions[0] as Record<string, unknown>;
    expect(read.reference).toBeUndefined();
    expect(read).toMatchObject({ position: 1, isBonus: false, bodyMd: "Body", choiceIds: ["a"], choices: { a: "Alpha" } });
    expect(parsed.tasks).toMatchObject({ storyMd: "S", schemaNotesMd: "" });
  });

  test("bounds a question's choices as the API does, at 50", () => {
    const withChoices = (n: number) => {
      const ids = Array.from({ length: n }, (_, i) => `c${i}`);
      const tasks = { lang: "en", story_md: "", schema_notes_md: "", questions: [{ ...question, choice_ids: ids }] };
      return archiveContestSchema.safeParse({ ...base, tasks }).success;
    };
    expect(withChoices(50)).toBe(true);
    expect(withChoices(51)).toBe(false);
  });

  test("bounds the questions", () => {
    const questions = Array.from({ length: 501 }, () => question);
    const tasks = { lang: "en", story_md: "", schema_notes_md: "", questions };
    expect(archiveContestSchema.safeParse({ ...base, tasks }).success).toBe(false);
  });
});
