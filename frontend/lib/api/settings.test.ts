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

// The API is the authority; this is the same rule, so the form can say so
// before a round trip and a link is never drawn from a value it would refuse.
describe("feedbackHref", () => {
  test.each([
    "https://forms.gle/beta",
    "https://docs.google.com/forms/d/e/1FAIpQLSf/viewform?usp=sf_link#top",
    "HTTPS://forms.gle/beta",
    "https://forms.gle/" + "a".repeat(FEEDBACK_URL_MAX - "https://forms.gle/".length),
  ])("takes %s", (value) => {
    expect(feedbackHref(value)).toBe(value);
  });

  test.each([
    ["nothing", ""],
    ["plain http", "http://forms.gle/beta"],
    ["a script", "javascript:alert(1)"],
    ["an inline document", "data:text/html,<script>alert(1)</script>"],
    ["no scheme", "forms.gle/beta"],
    ["a scheme-relative address", "//forms.gle/beta"],
    ["https without its slashes", "https:forms.gle/beta"],
    ["no host", "https://"],
    ["credentials", "https://user:secret@forms.gle/beta"],
    ["a host disguised as userinfo", "https://olympdb.usm.md@evil.example/"],
    ["a space", "https://forms.gle/be ta"],
    ["a backslash, which a browser reads as a slash", "https://forms.gle/\\evil.example/"],
    ["a trailing newline", "https://forms.gle/beta\n"],
    ["one character too many", "https://forms.gle/" + "a".repeat(FEEDBACK_URL_MAX + 1 - "https://forms.gle/".length)],
  ])("refuses %s", (_name, value) => {
    expect(feedbackHref(value)).toBeNull();
  });
});
