import { describe, expect, test } from "vitest";

import {
  accountListSchema,
  accountSchema,
  bulkPasswordResetResultSchema,
  bulkResultSchema,
  createdAccountSchema,
  importResultSchema,
  importSkippedRowSchema,
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
    const account = accountSchema.parse(wire);

    expect(account.email).toBeUndefined();
    expect(account.lastLoginAt).toBeUndefined();
  });

  test("keeps the flag that says the password is still the handover one", () => {
    const account = accountSchema.parse({ ...wire, must_change_password: true });

    expect(account.mustChangePassword).toBe(true);
  });

  test("refuses a status it has no wording for", () => {
    expect(() => accountSchema.parse({ ...wire, status: "banished" })).toThrow();
  });

  test("reads a soft-deleted account", () => {
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
    expect(account.statusChangedByLogin).toBe("a.admin");
  });

  test("reads an empty reason rather than an absent one, for an account nobody has touched", () => {
    // The account card gates its status panel on a non-empty `statusReason`.
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
    const parsed = bulkResultSchema.parse({ changed: [], skipped: [] });

    expect(parsed.changed).toEqual([]);
    expect(parsed.skipped).toEqual([]);
  });
});

describe("createdAccountSchema", () => {
  test("carries the account and the one-time password, exactly as the wire spells the second", () => {
    // Read by its wire name everywhere, so not transformed.
    const parsed = createdAccountSchema.parse({ user: wire, one_time_password: "Xk9-mQ2p" });

    expect(parsed.user).toMatchObject({ login: "s.popescu", fullName: "Sergiu Popescu" });
    expect(parsed.one_time_password).toBe("Xk9-mQ2p");
  });
});

describe("importSkippedRowSchema", () => {
  test("reads a row by its login alone — it never became an account, so there is no id to carry", () => {
    const skipped = importSkippedRowSchema.parse({ login: "s.popescu", reason: "login_taken" });

    expect(skipped).toEqual({ login: "s.popescu", reason: "login_taken" });
  });

  test("keeps a reason it does not recognise rather than rejecting the row", () => {
    const skipped = importSkippedRowSchema.parse({ login: "s.popescu", reason: "invented_later" });

    expect(skipped.reason).toBe("invented_later");
  });
});

describe("importResultSchema", () => {
  test("reads what an import created, one-time passwords included, and what it skipped", () => {
    const parsed = importResultSchema.parse({
      created: [{ user: wire, one_time_password: "Xk9-mQ2p" }],
      skipped: [{ login: "i.ivanov", reason: "invalid_row" }],
    });

    expect(parsed.created[0].user.login).toBe("s.popescu");
    expect(parsed.created[0].one_time_password).toBe("Xk9-mQ2p");
    expect(parsed.skipped[0]).toEqual({ login: "i.ivanov", reason: "invalid_row" });
  });

  test("reads an import that created nothing and skipped nothing", () => {
    const parsed = importResultSchema.parse({ created: [], skipped: [] });

    expect(parsed.created).toEqual([]);
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
