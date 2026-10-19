import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, test } from "vitest";

import { FEEDBACK_URL_MAX, SETTING_KEYS, feedbackHref, settingsSchema } from "./settings";

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
