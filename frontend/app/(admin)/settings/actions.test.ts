import { beforeEach, describe, expect, test, vi } from "vitest";

const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import { saveSettingsAction } from "./actions";

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("saveSettingsAction", () => {
  test("sends the feedback address under the API's key, trimmed", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await saveSettingsAction(
      {},
      form({ name: "USM", contact: "", feedback: "  https://forms.gle/beta  " }),
    );

    expect(serverRequest).toHaveBeenCalledWith("/settings", {
      method: "PUT",
      body: {
        values: {
          "installation.name": "USM",
          "installation.contact_email": "",
          "installation.feedback_url": "https://forms.gle/beta",
        },
      },
    });
    expect(state).toEqual({ saved: true });
  });

  test("sends an empty feedback address, which removes the link", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    await saveSettingsAction({}, form({ name: "USM", contact: "", feedback: "" }));

    expect(serverRequest.mock.calls[0][1].body.values["installation.feedback_url"]).toBe("");
  });

  test("refuses an address that is not https without calling the server", async () => {
    const state = await saveSettingsAction({}, form({ name: "USM", contact: "", feedback: "http://forms.gle/beta" }));

    expect(state).toEqual({ code: "invalid_feedback_url" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("passes the server's own refusal through by its code", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("invalid_feedback_url", 400, "no"));

    const state = await saveSettingsAction({}, form({ name: "USM", contact: "", feedback: "https://forms.gle/beta" }));

    expect(state).toEqual({ code: "invalid_feedback_url" });
  });
});
