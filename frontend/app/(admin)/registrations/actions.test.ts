import { beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { checkInAction } from "./actions";

const id = "11111111-1111-1111-1111-111111111111";

const wire = {
  id,
  first_name: "Ana",
  last_name: "Popescu",
  email: "ana@example.com",
  code: "K7M2QX",
  user_id: null,
  created_at: "2026-11-06T15:04:05Z",
  checked_in_at: "2026-11-06T18:00:00Z",
};

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("checkInAction", () => {
  test("marks the arrival and returns the updated registration", async () => {
    serverRequest.mockResolvedValueOnce(wire);

    const result = await checkInAction(id, true);

    expect(serverRequest).toHaveBeenCalledWith(`/registrations/${id}/check-in`, {
      method: "PUT",
      body: { checked_in: true },
    });
    expect(result.registration?.checkedInAt).toBe("2026-11-06T18:00:00Z");
    expect(revalidatePath).toHaveBeenCalledWith("/registrations");
  });

  test("never sends a forged id to the server", async () => {
    const result = await checkInAction("../auth/logout", true);

    expect(result).toEqual({ code: "registration_not_found" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("never sends anything but a boolean as the mark", async () => {
    const result = await checkInAction(id, "yes" as unknown as boolean);

    expect(result).toEqual({ code: "invalid_request" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("returns the API's code when the API refuses", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("registration_not_found", 404, "gone"));

    const result = await checkInAction(id, false);

    expect(result).toEqual({ code: "registration_not_found" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});
