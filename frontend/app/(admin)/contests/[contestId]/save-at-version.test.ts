import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/server")>()),
  serverRequest,
}));

import type { RequestOptions } from "@/lib/api/client";
import { CONTEST_VERSION_HEADER } from "@/lib/api/contests";

import { saveAtVersion } from "./save-at-version";

const contestId = "11111111-1111-1111-1111-111111111111";

function renderedAt(version: string): FormData {
  const data = new FormData();
  data.set("expectedVersion", version);
  return data;
}

/** An API that stores the save and names the version in its header. */
function answering(header: string | null) {
  serverRequest.mockImplementationOnce(async (_path: string, init: RequestOptions) => {
    init.onHeaders?.(new Headers(header === null ? {} : { [CONTEST_VERSION_HEADER]: header }));
    return undefined;
  });
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("saveAtVersion, the version the save stored", () => {
  test("comes back with the answer that worked", async () => {
    answering("4");

    expect(await saveAtVersion(contestId, renderedAt("3"), `/contests/${contestId}/story`, { method: "PUT" })).toEqual({
      ok: true,
      version: 4,
    });
  });

  test.each([
    ["absent", null],
    ["not a whole number", "4.5"],
    ["signed", "+4"],
  ])("is left out when the answer's is %s", async (_name, header) => {
    answering(header);

    expect(await saveAtVersion(contestId, renderedAt("3"), `/contests/${contestId}/story`, { method: "PUT" })).toEqual({
      ok: true,
    });
  });
});
