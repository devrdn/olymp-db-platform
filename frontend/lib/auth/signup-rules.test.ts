import { describe, expect, test } from "vitest";

import {
  checkSignup,
  fullNameFits,
  isPlainLogin,
  isSignupCode,
  isWrittenAsName,
  normalizeCode,
  normalizeLogin,
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

  test.each(["", "Ana1", "ana@example.com", "J.R. Tolkien", "<b>Ana</b>"])("refuses %s", (name) => {
    expect(isWrittenAsName(name)).toBe(false);
  });
});

/** `users.MaxFullNameLength` is checked with `len`, which counts UTF-8 bytes. */
describe("the full-name bound", () => {
  test("holds 200 bytes", () => {
    expect(fullNameFits("A".repeat(200))).toBe(true);
    expect(fullNameFits("A".repeat(201))).toBe(false);
  });

  test("counts a Cyrillic letter as the two bytes it takes", () => {
    expect(fullNameFits("Я".repeat(100))).toBe(true);
    expect(fullNameFits("Я".repeat(101))).toBe(false);
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

  test("names the login rule when the login breaks it", () => {
    expect(checkSignup({ ...filled, login: "a.b" })).toEqual({ ok: false, code: "invalid_request", broken: "login" });
  });

  test("names the full-name rule when the name is not written as one", () => {
    expect(checkSignup({ ...filled, fullName: "Ana 2" })).toEqual({
      ok: false,
      code: "invalid_request",
      broken: "fullName",
    });
  });

  test("says a long Cyrillic name is too long, not that it breaks the rule", () => {
    const name = "Александра Константиновна Преображенская-Воскресенская ".repeat(3).trim();
    expect([...name].length).toBeLessThanOrEqual(200);

    expect(checkSignup({ ...filled, fullName: name })).toEqual({
      ok: false,
      code: "invalid_request",
      broken: "fullNameLength",
    });
  });

  /** `users.MinPasswordLength` counts characters (runes), and is the policy's only rule. */
  test("names the password minimum when the password is shorter than 12 characters", () => {
    expect(checkSignup({ ...filled, password: "elevenchars", confirm: "elevenchars" })).toEqual({
      ok: false,
      code: "weak_password",
      broken: "passwordLength",
    });
  });

  test("counts the password minimum in characters", () => {
    expect(checkSignup({ ...filled, password: "țțțțțțțțțțțț", confirm: "țțțțțțțțțțțț" }).ok).toBe(true);
  });

  test("says the two passwords differ", () => {
    expect(checkSignup({ ...filled, confirm: "something else" })).toEqual({ ok: false, code: "password_mismatch" });
  });

  test("leaves the email to the API", () => {
    expect(checkSignup({ ...filled, email: "not an address" }).ok).toBe(true);
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
