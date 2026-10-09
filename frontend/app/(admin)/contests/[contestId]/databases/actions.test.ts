import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import { dropGameInstanceAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("dropGameInstanceAction", () => {
  test("deletes the named database of the named contest, and revalidates", async () => {
    serverRequest.mockResolvedValueOnce({ database: "game_c1_u1", status: "dropped" });

    const state = await dropGameInstanceAction({}, form({ contestId, database: "game_c1_u1" }));

    expect(state).toEqual({ saved: true });
    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/game/instances/game_c1_u1`,
      { method: "DELETE" },
    );
    // The list is server-rendered; without this the dropped row would remain.
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("never sends a request for a contest segment that is not an identifier", async () => {
    const state = await dropGameInstanceAction({}, form({ contestId: "nope", database: "game_x" }));

    expect(state).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("refuses an empty database name without asking the server about it", async () => {
    const state = await dropGameInstanceAction({}, form({ contestId, database: "" }));

    expect(state).toEqual({ code: "game_instance_not_found" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  // The code is passed through; the row's message is chosen from it.
  test("carries the server's own refusal code back to the row", async () => {
    serverRequest.mockRejectedValueOnce(
      new ApiError("game_instance_already_dropped", 409, "already gone"),
    );

    const state = await dropGameInstanceAction({}, form({ contestId, database: "game_c1_u1" }));

    expect(state).toEqual({ code: "game_instance_already_dropped" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("reports an unreachable API as such rather than as a refusal", async () => {
    serverRequest.mockRejectedValueOnce(new Error("connect ECONNREFUSED"));

    const state = await dropGameInstanceAction({}, form({ contestId, database: "game_c1_u1" }));

    expect(state).toEqual({ code: "unreachable" });
  });
});
