import { describe, expect, test } from "vitest";

import {
  checkSignup,
  isPlainLogin,
  isSignupCode,
  isWrittenAsName,
  normalizeCode,
  normalizeLogin,
  showableEmail,
} from "./signup-rules";

const filled = {
  fullName: " Ana Popescu ",
  login: " Ana_P ",
  email: " ana@example.com ",
  password: "a long enough secret",
  confirm: "a long enough secret",
};

describe("the login rule", () => {
  test.each(["ana", "a1-b_c", "0day", "a".repeat(64)])("accepts %s", (login) => {
    expect(isPlainLogin(login)).toBe(true);
  });

  test.each(["ab", "_ana", "-ana", "ana.p", "Ana", "ана", "a".repeat(65), "ana p"])("refuses %s", (login) => {
    expect(isPlainLogin(login)).toBe(false);
  });

  test("lowers the case and trims, as the API keys it", () => {
    expect(normalizeLogin("  Ana_P ")).toBe("ana_p");
  });
});

describe("the full-name rule", () => {
  test.each(["Ana Popescu", "Jean-Luc O'Neil", "J. R. Tolkien", "Ана Попеску", "Ștefan Țurcan"])(
    "accepts %s",
    (name) => {
      expect(isWrittenAsName(name)).toBe(true);
    },
  );

  test.each(["", "Ana1", "ana@example.com", "J.R. Tolkien", "<b>Ana</b>", "A".repeat(201)])("refuses %s", (name) => {
    expect(isWrittenAsName(name)).toBe(false);
  });

  test("counts the bound in characters, not bytes", () => {
    expect(isWrittenAsName("Ș".repeat(200))).toBe(true);
  });
});

describe("checkSignup", () => {
  test("hands on the trimmed fields and the password untouched", () => {
    const check = checkSignup({ ...filled, password: " spaced secret ", confirm: " spaced secret " });

    expect(check).toEqual({
      ok: true,
      command: { full_name: "Ana Popescu", login: "ana_p", email: "ana@example.com", password: " spaced secret " },
    });
  });

  test("blames the login when it breaks the rule", () => {
    expect(checkSignup({ ...filled, login: "a.b" })).toEqual({ ok: false, code: "invalid_request", field: "login" });
  });

  test("blames the full name when it is not written as a name", () => {
    expect(checkSignup({ ...filled, fullName: "Ana 2" })).toEqual({
      ok: false,
      code: "invalid_request",
      field: "fullName",
    });
  });

  test("says the two passwords differ before anything else about them", () => {
    expect(checkSignup({ ...filled, confirm: "something else" })).toEqual({ ok: false, code: "password_mismatch" });
  });

  test("leaves the email and the password's strength to the API", () => {
    expect(checkSignup({ ...filled, email: "not an address", password: "short", confirm: "short" }).ok).toBe(true);
  });
});

describe("the confirmation code", () => {
  test("reads a pasted code with spaces as its six digits", () => {
    expect(normalizeCode(" 123 456 ")).toBe("123456");
  });

  test.each([
    ["123456", true],
    ["12345", false],
    ["1234567", false],
    ["12a456", false],
  ])("%s is a code: %s", (code, expected) => {
    expect(isSignupCode(code)).toBe(expected);
  });
});

describe("showableEmail", () => {
  test("shows an address", () => {
    expect(showableEmail("ana@example.com")).toBe("ana@example.com");
  });

  /** The address bar may only select a sentence, never write one. */
  test.each(["Call 0800 now", "", "a".repeat(250) + "@x.md", "ana@example.com\nsee"])("withholds %j", (raw) => {
    expect(showableEmail(raw)).toBeUndefined();
  });
});
