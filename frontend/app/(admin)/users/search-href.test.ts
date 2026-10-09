import { describe, expect, test } from "vitest";

import { accountsHref } from "./search-href";

describe("accountsHref", () => {
  test("is the bare address when nothing is being asked", () => {
    // Empty parameters are left out.
    expect(accountsHref({ query: "", status: "" })).toBe("/users");
  });

  test("carries what is being asked", () => {
    expect(accountsHref({ query: "popescu", status: "blocked" })).toBe(
      "/users?q=popescu&status=blocked",
    );
  });

  test("escapes what a person typed", () => {
    // A space or ampersand must not become a second parameter.
    expect(accountsHref({ query: "a&b c" })).toBe("/users?q=a%26b+c");
  });

  test("keeps a page only when there is one", () => {
    expect(accountsHref({ query: "popescu", offset: 50 })).toBe("/users?q=popescu&offset=50");
    expect(accountsHref({ query: "popescu", offset: 0 })).toBe("/users?q=popescu");
  });

  test("drops the page whenever the question changes", () => {
    // A new search drops the page, or it could land past the end of a short
    // result.
    expect(accountsHref({ query: "ivanov", offset: 100, resetPage: true })).toBe("/users?q=ivanov");
  });
});
