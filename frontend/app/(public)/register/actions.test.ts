import { beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { registerAction } from "./actions";

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

const filled = { first_name: " Ana ", last_name: "Popescu", email: " ana@example.com " };

beforeEach(() => {
  serverRequest.mockReset();
});

describe("registerAction", () => {
  test("sends the trimmed fields and returns the outcome", async () => {
    serverRequest.mockResolvedValueOnce({ status: "existing", email: "ana@example.com", email_sent: true });

    const state = await registerAction({}, form(filled));

    expect(serverRequest).toHaveBeenCalledWith("/public/registrations", {
      method: "POST",
      body: { first_name: "Ana", last_name: "Popescu", email: "ana@example.com" },
    });
    expect(state).toEqual({ outcome: { status: "existing", email: "ana@example.com", emailSent: true } });
  });

  test("refuses an empty name without calling the server, keeping what was typed", async () => {
    const state = await registerAction({}, form({ ...filled, last_name: "  " }));

    expect(serverRequest).not.toHaveBeenCalled();
    expect(state).toEqual({
      code: "registration_invalid_name",
      values: { firstName: "Ana", lastName: "", email: "ana@example.com" },
    });
  });

  test("refuses an empty email without calling the server", async () => {
    const state = await registerAction({}, form({ ...filled, email: "" }));

    expect(serverRequest).not.toHaveBeenCalled();
    expect(state.code).toBe("registration_invalid_email");
  });

  test("returns the API's code and the typed values when the API refuses", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("registration_full", 409, "full"));

    const state = await registerAction({}, form(filled));

    expect(state).toEqual({
      code: "registration_full",
      values: { firstName: "Ana", lastName: "Popescu", email: "ana@example.com" },
    });
  });

  test("reports an API that never answered as unreachable", async () => {
    serverRequest.mockRejectedValueOnce(new TypeError("fetch failed"));

    const state = await registerAction({}, form(filled));

    expect(state.code).toBe("unreachable");
  });
});
