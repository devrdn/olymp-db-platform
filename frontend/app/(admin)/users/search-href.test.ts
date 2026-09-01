import { describe, expect, test } from "vitest";

import { accountsHref } from "./search-href";

describe("accountsHref", () => {
  test("is the bare address when nothing is being asked", () => {
    // A URL full of empty parameters is a URL nobody wants to share, and it
    // makes "am I filtering?" a question about string contents.
    expect(accountsHref({ query: "", status: "" })).toBe("/users");
  });

  test("carries what is being asked", () => {
    expect(accountsHref({ query: "popescu", status: "blocked" })).toBe(
      "/users?q=popescu&status=blocked",
    );
  });

  test("escapes what a person typed", () => {
    // The query goes into an address. A space or an ampersand typed into the
    // box must not become a second parameter.
    expect(accountsHref({ query: "a&b c" })).toBe("/users?q=a%26b+c");
  });

  test("keeps a page only when there is one", () => {
    expect(accountsHref({ query: "popescu", offset: 50 })).toBe("/users?q=popescu&offset=50");
    expect(accountsHref({ query: "popescu", offset: 0 })).toBe("/users?q=popescu");
  });

  test("drops the page whenever the question changes", () => {
    // The trap this exists to close. Searching from page three of the old
    // result leaves somebody on page three of a result with four rows — an
    // empty screen that says nothing matched, when plenty did.
    expect(accountsHref({ query: "ivanov", offset: 100, resetPage: true })).toBe("/users?q=ivanov");
  });
});
