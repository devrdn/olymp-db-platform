import { describe, expect, test } from "vitest";

import { registrationListSchema, registrationOutcomeSchema } from "./registrations";

const row = {
  id: "11111111-1111-1111-1111-111111111111",
  first_name: "Ana",
  last_name: "Popescu",
  email: "ana@example.com",
  code: "K7M2QX",
  user_id: null,
  created_at: "2026-11-06T15:04:05Z",
  checked_in_at: null,
};

describe("registrationOutcomeSchema", () => {
  test("reads a new registration with its account", () => {
    expect(
      registrationOutcomeSchema.parse({
        status: "created",
        code: "K7M2QX",
        first_name: "Ana",
        last_name: "Popescu",
        email: "ana@example.com",
        account: "created",
        login: "ana@example.com",
        one_time_password: "secret-1",
        email_sent: true,
      }),
    ).toEqual({
      status: "created",
      code: "K7M2QX",
      firstName: "Ana",
      lastName: "Popescu",
      email: "ana@example.com",
      account: "created",
      login: "ana@example.com",
      oneTimePassword: "secret-1",
      emailSent: true,
    });
  });

  test("reads an existing account as one with no login or password to show", () => {
    const outcome = registrationOutcomeSchema.parse({
      status: "created",
      code: "K7M2QX",
      first_name: "Ana",
      last_name: "Popescu",
      email: "ana@example.com",
      account: "existing",
      email_sent: false,
    });

    expect(outcome).toMatchObject({ status: "created", account: "existing", emailSent: false });
    expect(outcome).not.toHaveProperty("oneTimePassword", expect.anything());
  });

  test("reads a repeat registration, which carries no code", () => {
    expect(
      registrationOutcomeSchema.parse({ status: "existing", email: "ana@example.com", email_sent: true }),
    ).toEqual({ status: "existing", email: "ana@example.com", emailSent: true });
  });
});

describe("registrationListSchema", () => {
  test("reads the rows and the counters, with nulls as absent", () => {
    const list = registrationListSchema.parse({
      registrations: [row, { ...row, id: "2", user_id: "u", checked_in_at: "2026-11-06T18:00:00Z" }],
      total: 2,
      checked_in: 1,
    });

    expect(list.total).toBe(2);
    expect(list.checkedIn).toBe(1);
    expect(list.registrations[0]).toEqual({
      id: row.id,
      firstName: "Ana",
      lastName: "Popescu",
      email: "ana@example.com",
      code: "K7M2QX",
      userId: undefined,
      createdAt: row.created_at,
      checkedInAt: undefined,
    });
    expect(list.registrations[1].checkedInAt).toBe("2026-11-06T18:00:00Z");
  });
});
