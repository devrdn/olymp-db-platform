import { describe, expect, test } from "vitest";

import {
  accountListSchema,
  accountSchema,
  bulkPasswordResetResultSchema,
  bulkResultSchema,
  roleListSchema,
  skippedAccountSchema,
} from "./accounts";

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

  test("reads a soft-deleted account", () => {
    // The row stays, only its status moves: deleted is an ordinary status the
    // list renders, not an account that disappears from the API's answers.
    const account = accountSchema.parse({ ...wire, status: "deleted" });

    expect(account.status).toBe("deleted");
  });

  test("carries what explains a status change, when there was one", () => {
    const account = accountSchema.parse({
      ...wire,
      status: "blocked",
      status_reason: "cheating in the October contest",
      status_changed_at: "2026-03-02T09:00:00Z",
      status_changed_by: "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6",
      status_changed_by_login: "a.admin",
    });

    expect(account.statusReason).toBe("cheating in the October contest");
    expect(account.statusChangedAt).toBe("2026-03-02T09:00:00Z");
    expect(account.statusChangedBy).toBe("9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6");
    // Resolved by the server's own query — a LEFT JOIN in
    // `internal/postgres/users.go` — so the card never has to ask a second
    // time for the one login it needs.
    expect(account.statusChangedByLogin).toBe("a.admin");
  });

  test("reads an empty reason rather than an absent one, for an account nobody has touched", () => {
    // The four fields are omitted on the wire (see `UserResponse` in
    // `backend/internal/api/users_handler.go`), and the account card gates its
    // status panel on `statusReason` being non-empty — a string it can always
    // compare, not an optional it must first check for presence.
    const account = accountSchema.parse(wire);

    expect(account.statusReason).toBe("");
    expect(account.statusChangedAt).toBeUndefined();
    expect(account.statusChangedBy).toBeUndefined();
    expect(account.statusChangedByLogin).toBe("");
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

describe("skippedAccountSchema", () => {
  test("reads a reason from the known vocabulary", () => {
    const skipped = skippedAccountSchema.parse({
      id: "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6",
      login: "ivanov",
      reason: "last_administrator",
    });

    expect(skipped.reason).toBe("last_administrator");
  });

  test("keeps a reason it does not recognise rather than rejecting the row", () => {
    // A newer backend can ship a skip reason before this build knows the
    // word for it. The row is still an outcome the administrator has to see,
    // so parsing must not throw and must not drop the field.
    const skipped = skippedAccountSchema.parse({
      id: "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6",
      login: "ivanov",
      reason: "invented_later",
    });

    expect(skipped.reason).toBe("invented_later");
  });
});

describe("bulkResultSchema", () => {
  test("reads a bulk result, unknown skip reasons included", () => {
    const parsed = bulkResultSchema.parse({
      changed: ["8f1a0c3e-2b44-4e77-8d0a-1c5b8e91a4d6"],
      skipped: [
        { id: "1a2b3c4d-2b44-4e77-8d0a-1c5b8e91a4d6", login: "ivanov", reason: "invented_later" },
      ],
    });

    expect(parsed.changed).toEqual(["8f1a0c3e-2b44-4e77-8d0a-1c5b8e91a4d6"]);
    expect(parsed.skipped[0].reason).toBe("invented_later");
  });

  test("reads an empty selection outcome", () => {
    // Every id in the request could be skipped; that is a 200 with an empty
    // `changed`, not an error, and the schema must not require a non-empty
    // list.
    const parsed = bulkResultSchema.parse({ changed: [], skipped: [] });

    expect(parsed.changed).toEqual([]);
    expect(parsed.skipped).toEqual([]);
  });
});

describe("bulkPasswordResetResultSchema", () => {
  test("reads the passwords issued, in the interface's own casing", () => {
    const parsed = bulkPasswordResetResultSchema.parse({
      issued: [
        {
          id: "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6",
          login: "s.popescu",
          one_time_password: "Xk9-mQ2p",
        },
      ],
      skipped: [
        { id: "1a2b3c4d-2b44-4e77-8d0a-1c5b8e91a4d6", login: "deleted.one", reason: "deleted" },
      ],
    });

    expect(parsed.issued[0]).toMatchObject({ login: "s.popescu", oneTimePassword: "Xk9-mQ2p" });
    expect(parsed.skipped[0].reason).toBe("deleted");
  });
});
