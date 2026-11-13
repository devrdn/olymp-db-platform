import { beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { enrollAction } from "./actions";

const contestId = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";

function form(id: string = contestId): FormData {
  const data = new FormData();
  data.set("contestId", id);
  return data;
}

/** The enrolment refuses with `code`; the methods answer names `domain` and whether recovery is on. */
function refuses(code: string, domain: string | Error, reset = false) {
  serverRequest.mockImplementation(async (path: string) => {
    if (path === `/contests/${contestId}/enroll`) throw new ApiError(code, 403, "refused");
    if (path === "/auth/methods") {
      if (domain instanceof Error) throw domain;
      return { password_signup: true, password_reset: reset, microsoft: false, trusted_email_domain: domain, providers: [] };
    }
    throw new Error(`unexpected ${path}`);
  });
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
  vi.spyOn(console, "warn").mockImplementation(() => {});
});

describe("enrollAction", () => {
  test("enrols and refreshes the register", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    expect(await enrollAction({}, form())).toEqual({ enrolled: true });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/enroll`, { method: "POST" });
    expect(revalidatePath).toHaveBeenCalledWith("/my");
  });

  test("never sends a forged contest id", async () => {
    expect(await enrollAction({}, form("../users"))).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("names the site's domain beside a refusal for an address outside it", async () => {
    refuses("contest_trusted_email_required", "example.edu");

    expect(await enrollAction({}, form())).toEqual({
      code: "contest_trusted_email_required",
      domain: "example.edu",
    });
  });

  /** A staff-made account may already hold such an address, unconfirmed; a reset link confirms it. */
  test("says whether a reset link can confirm the address", async () => {
    refuses("contest_trusted_email_required", "example.edu", true);

    expect(await enrollAction({}, form())).toEqual({
      code: "contest_trusted_email_required",
      domain: "example.edu",
      confirmByReset: true,
    });
  });

  test("gives the refusal alone when the site has no domain, or cannot say", async () => {
    refuses("contest_trusted_email_required", "");
    expect(await enrollAction({}, form())).toEqual({ code: "contest_trusted_email_required" });

    refuses("contest_trusted_email_required", new ApiError("unreachable", 502, "down"));
    expect(await enrollAction({}, form())).toEqual({ code: "contest_trusted_email_required" });
  });

  test("asks for no domain on any other refusal", async () => {
    refuses("enrollment_closed", "example.edu");

    expect(await enrollAction({}, form())).toEqual({ code: "enrollment_closed" });
    expect(serverRequest).toHaveBeenCalledTimes(1);
  });
});
