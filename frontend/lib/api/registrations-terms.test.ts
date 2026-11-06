import { describe, expect, test } from "vitest";

import { codeForLookup, displayCode, matchesRegistration, type Registration } from "./registrations-terms";

describe("displayCode", () => {
  test("splits the stored code in two halves", () => {
    expect(displayCode("K7M2QX")).toBe("K7M-2QX");
  });
});

describe("codeForLookup", () => {
  test("ignores case, spaces and dashes", () => {
    expect(codeForLookup(" k7m - 2qx ")).toBe("K7M2QX");
  });
});

describe("matchesRegistration", () => {
  const registration: Registration = {
    id: "11111111-1111-1111-1111-111111111111",
    firstName: "Ana",
    lastName: "Popescu",
    email: "ana@example.com",
    code: "K7M2QX",
    createdAt: "2026-11-06T15:04:05Z",
  };

  test.each([
    ["an empty query", "  "],
    ["the code as printed", "K7M-2QX"],
    ["the code in lower case with a space", "k7m 2qx"],
    ["part of the code", "2qx"],
    ["the first name in any case", "ana"],
    ["the last name", "POPESCU"],
    ["the full name", "Ana Popescu"],
    ["the name in the other order", "popescu ana"],
    ["part of the email", "@example"],
  ])("finds the row by %s", (_, query) => {
    expect(matchesRegistration(registration, query)).toBe(true);
  });

  test.each([
    ["another code", "ZZZ-999"],
    ["another name", "Ion"],
  ])("does not find the row by %s", (_, query) => {
    expect(matchesRegistration(registration, query)).toBe(false);
  });
});
