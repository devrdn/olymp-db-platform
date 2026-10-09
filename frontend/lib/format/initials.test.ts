import { describe, expect, test } from "vitest";

import { initials } from "./initials";

describe("initials", () => {
  test("takes the first letter of the first and last name", () => {
    expect(initials("Ivan Ivanov")).toBe("II");
    expect(initials("Sergiu Popescu")).toBe("SP");
  });

  test("skips the middle, which is where a patronymic sits", () => {
    expect(initials("Иван Сергеевич Иванов")).toBe("ИИ");
  });

  test("works in every alphabet the interface speaks", () => {
    expect(initials("Ștefan Ionescu")).toBe("ȘI");
  });

  test("gives one letter to a single name rather than doubling it", () => {
    expect(initials("Cher")).toBe("C");
  });

  test("falls back to the login when there is no name yet", () => {
    expect(initials("", "s.popescu")).toBe("SP");
    expect(initials("   ", "ivanov")).toBe("I");
  });

  test("is empty when there is nothing to take a letter from", () => {
    expect(initials("", "")).toBe("");
  });

  test("ignores the punctuation a login is built from", () => {
    expect(initials("", "s.popescu")).toBe("SP");
    expect(initials("", "i_ivanov")).toBe("II");
  });
});
