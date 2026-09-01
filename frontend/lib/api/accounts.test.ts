import { describe, expect, test } from "vitest";

import { accountListSchema, accountSchema, roleListSchema } from "./accounts";

const wire = {
  id: "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6",
  login: "s.popescu",
  full_name: "Sergiu Popescu",
  status: "active",
  roles: ["student"],
  must_change_password: false,
  created_at: "2026-03-01T10:00:00Z",
};

describe("accountSchema", () => {
  test("reads the account the API describes", () => {
    const account = accountSchema.parse({ ...wire, email: "s@example.edu" });

    expect(account).toMatchObject({
      login: "s.popescu",
      fullName: "Sergiu Popescu",
      status: "active",
      roles: ["student"],
      email: "s@example.edu",
    });
  });

  test("survives the fields the API leaves out", () => {
    // `email` and `last_login_at` are omitempty on the wire: an account with
    // no address and one that has never signed in are ordinary, and a schema
    // that required them would fail the whole page over a blank cell.
    const account = accountSchema.parse(wire);

    expect(account.email).toBeUndefined();
    expect(account.lastLoginAt).toBeUndefined();
  });

  test("keeps the flag that says the password is still the handover one", () => {
    // The screen marks those accounts: an administrator who reset a password
    // needs to see who has not yet picked their own.
    const account = accountSchema.parse({ ...wire, must_change_password: true });

    expect(account.mustChangePassword).toBe(true);
  });

  test("refuses a status it has no wording for", () => {
    // Better a loud failure at the boundary than a row rendering an empty
    // badge three components later.
    expect(() => accountSchema.parse({ ...wire, status: "banished" })).toThrow();
  });
});

describe("accountListSchema", () => {
  test("carries the total, which is what paging is counted from", () => {
    const page = accountListSchema.parse({ items: [wire], total: 137 });

    expect(page.total).toBe(137);
    expect(page.items).toHaveLength(1);
  });
});

describe("roleListSchema", () => {
  test("reads the catalogue the server publishes", () => {
    // Never a hard-coded list: roles are rows so that adding one is data.
    const roles = roleListSchema.parse({
      items: [
        { code: "student", name: "Student" },
        { code: "admin", name: "System administrator" },
      ],
    });

    expect(roles.items.map((role) => role.code)).toEqual(["student", "admin"]);
  });
});
