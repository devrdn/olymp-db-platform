import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, test } from "vitest";

import { FEEDBACK_URL_MAX, MAIL_LANGUAGES, SETTING_KEYS, feedbackHref, settingsSchema } from "./settings";

describe("the settings payload", () => {
  test("carries the feedback address under its own name", () => {
    const settings = settingsSchema.parse({
      values: { [SETTING_KEYS.feedback]: "https://forms.gle/beta" },
    });

    expect(settings.feedback).toBe("https://forms.gle/beta");
  });

  test("reads an unset feedback address as empty", () => {
    expect(settingsSchema.parse({ values: {} }).feedback).toBe("");
  });

  test("carries the mail language under its own name", () => {
    const settings = settingsSchema.parse({ values: { [SETTING_KEYS.mailLanguage]: "ru" } });

    expect(settings.mailLanguage).toBe("ru");
  });

  // The public read leaves it out; only the administrator's carries it.
  test("reads an absent mail language as empty", () => {
    expect(settingsSchema.parse({ values: {} }).mailLanguage).toBe("");
  });
});

// The API's own table (backend/internal/settings/testdata), so the two sides
// keep one rule: a value the form lets through is one the API takes, and a
// stored value the API took is one the link draws.
type FeedbackCase = { name: string; value: string };
const shared = JSON.parse(
  readFileSync(resolve(__dirname, "../../../backend/internal/settings/testdata/feedback-urls.json"), "utf8"),
) as { accept: FeedbackCase[]; refuse: FeedbackCase[] };

const longest = "https://forms.gle/" + "a".repeat(FEEDBACK_URL_MAX - "https://forms.gle/".length);

describe("feedbackHref", () => {
  test.each([...shared.accept, { name: "the longest allowed", value: longest }])("takes $name", ({ value }) => {
    expect(feedbackHref(value)).toBe(value);
  });

  test.each([
    ...shared.refuse,
    { name: "nothing", value: "" },
    { name: "one byte past the longest", value: longest + "a" },
  ])("refuses $name", ({ value }) => {
    expect(feedbackHref(value)).toBeNull();
  });
});

// The API writes a message in a language only when its texts are there, one
// directory per language, so the screen offers exactly those.
test("the mail languages are the ones the API has texts in", () => {
  const written = readdirSync(resolve(__dirname, "../../../backend/internal/mailing/texts")).sort();

  expect([...MAIL_LANGUAGES].sort()).toEqual(written);
});
