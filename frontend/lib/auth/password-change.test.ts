import { describe, expect, test } from "vitest";

import { checkPasswordChange } from "./password-change";

describe("checkPasswordChange", () => {
  test("turns a filled form into the command the API takes", () => {
    const result = checkPasswordChange({
      current: "handover-secret",
      next: "a longer phrase entirely",
      confirm: "a longer phrase entirely",
    });

    expect(result).toEqual({
      ok: true,
      command: { old_password: "handover-secret", new_password: "a longer phrase entirely" },
    });
  });

  test("refuses a form where the confirmation does not match", () => {
    // The one rule the server cannot check: it never sees the second field.
    expect(
      checkPasswordChange({ current: "one-time", next: "chosen one", confirm: "chosen won" }),
    ).toEqual({ ok: false, code: "password_mismatch" });
  });

  test("refuses an empty field before spending a request on it", () => {
    for (const input of [
      { current: "", next: "chosen one", confirm: "chosen one" },
      { current: "one-time", next: "", confirm: "" },
    ]) {
      expect(checkPasswordChange(input)).toEqual({ ok: false, code: "invalid_request" });
    }
  });

  test("calls an unfilled confirmation a mismatch, not an empty field", () => {
    // It is both, and "the two do not match" is the sentence that points at
    // the field the author still has to fill.
    expect(
      checkPasswordChange({ current: "one-time", next: "chosen one", confirm: "" }),
    ).toEqual({ ok: false, code: "password_mismatch" });
  });

  test("reports the mismatch rather than the emptiness when both are true", () => {
    // "The two do not match" is the actionable half; being told a field is
    // empty when the real problem is a typo in the other one sends the author
    // looking at the wrong field.
    expect(
      checkPasswordChange({ current: "one-time", next: "chosen one", confirm: "chosen" }),
    ).toEqual({ ok: false, code: "password_mismatch" });
  });

  test("does not trim a password", () => {
    // A leading or trailing space is a character of the secret. Trimming it
    // here would silently change what the account was handed.
    const result = checkPasswordChange({
      current: " padded secret ",
      next: " chosen phrase ",
      confirm: " chosen phrase ",
    });

    expect(result).toEqual({
      ok: true,
      command: { old_password: " padded secret ", new_password: " chosen phrase " },
    });
  });

  test("leaves strength to the server", () => {
    // Duplicating the rule here is a second source of truth that drifts from
    // the Go one the moment either moves. A weak password reaches the API and
    // comes back as `weak_password`, which the dictionary already translates.
    expect(checkPasswordChange({ current: "a", next: "b", confirm: "b" }).ok).toBe(true);
  });
});
