import { beforeEach, describe, expect, test } from "vitest";
import { vi } from "vitest";

// `vi.mock` factories are hoisted, so mocks are built with `vi.hoisted`. Only
// `serverRequest` is mocked, so the real request bodies and zod parsing in
// `lib/api/accounts.ts` are exercised.
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import { MAX_IMPORT_ROWS } from "@/lib/api/accounts-terms";

import { createAccountAction, importAccountsAction } from "./create-actions";

function form(fields: Record<string, string | string[]>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) {
    if (Array.isArray(value)) {
      for (const one of value) data.append(key, one);
    } else {
      data.set(key, value);
    }
  }
  return data;
}

const wireAccount = {
  id: "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6",
  login: "s.popescu",
  full_name: "Sergiu Popescu",
  status: "active",
  roles: ["student"],
  must_change_password: true,
  created_at: "2026-03-01T10:00:00Z",
};

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("createAccountAction", () => {
  test("refuses an empty login without ever calling the server", async () => {
    const state = await createAccountAction({}, form({ login: "  ", full_name: "Sergiu Popescu" }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses an empty full name without ever calling the server", async () => {
    const state = await createAccountAction({}, form({ login: "s.popescu", full_name: "  " }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("sends the trimmed fields and the checked roles, and returns the one-time password", async () => {
    serverRequest.mockResolvedValueOnce({ user: wireAccount, one_time_password: "swordfish-1" });

    const state = await createAccountAction(
      {},
      form({
        login: "  s.popescu  ",
        full_name: "  Sergiu Popescu  ",
        email: " s@example.edu ",
        roles: ["student"],
      }),
    );

    expect(serverRequest).toHaveBeenCalledWith("/users", {
      method: "POST",
      body: { login: "s.popescu", full_name: "Sergiu Popescu", email: "s@example.edu", roles: ["student"] },
    });
    expect(state.result?.one_time_password).toBe("swordfish-1");
    expect(state.result?.user.login).toBe("s.popescu");
    expect(revalidatePath).toHaveBeenCalledWith("/users", "layout");
  });

  // A server refusal keeps its own code, which the component translates.
  test("passes a taken-login refusal through by its own code", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("login_taken", 409, "This login is already in use"));

    const state = await createAccountAction(
      {},
      form({ login: "s.popescu", full_name: "Sergiu Popescu" }),
    );

    expect(state).toEqual({ code: "login_taken" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("reports an unreachable server distinctly from a refusal it explained", async () => {
    serverRequest.mockRejectedValueOnce(new TypeError("fetch failed"));

    const state = await createAccountAction(
      {},
      form({ login: "s.popescu", full_name: "Sergiu Popescu" }),
    );

    expect(state).toEqual({ code: "unreachable" });
  });
});

describe("importAccountsAction", () => {
  test("refuses an empty paste without ever calling the server", async () => {
    const state = await importAccountsAction({}, form({ roster: "   \n  " }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("sends every parsed row and the checked roles, and reports what was created", async () => {
    serverRequest.mockResolvedValueOnce({
      created: [{ user: wireAccount, one_time_password: "swordfish-1" }],
      skipped: [{ login: "i.ivanov", reason: "login_taken" }],
    });

    const state = await importAccountsAction(
      {},
      form({
        roster: "s.popescu, Sergiu Popescu\ni.ivanov, Ivan Ivanov",
        roles: ["student"],
      }),
    );

    expect(serverRequest).toHaveBeenCalledWith("/users/import", {
      method: "POST",
      body: {
        rows: [
          { login: "s.popescu", full_name: "Sergiu Popescu", email: "" },
          { login: "i.ivanov", full_name: "Ivan Ivanov", email: "" },
        ],
        roles: ["student"],
      },
    });
    expect(state.result?.created).toHaveLength(1);
    expect(state.result?.created[0]?.one_time_password).toBe("swordfish-1");
    expect(state.result?.skipped).toEqual([{ login: "i.ivanov", reason: "login_taken" }]);
    expect(revalidatePath).toHaveBeenCalledWith("/users", "layout");
  });

  test("reports a server refusal by its own code rather than a generic failure", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("invalid_request", 400, "too many rows"));

    const state = await importAccountsAction({}, form({ roster: "s.popescu, Sergiu Popescu" }));

    expect(state).toEqual({ code: "invalid_request" });
  });

  test("refuses a roster larger than one import carries, without ever calling the server", async () => {
    // Mirrors `users.maxImportRows`; refused before a request.
    const roster = Array.from({ length: MAX_IMPORT_ROWS + 1 }, (_, i) => `s${i}, Student ${i}`).join("\n");

    const state = await importAccountsAction({}, form({ roster }));

    expect(state).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
