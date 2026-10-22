import { beforeEach, describe, expect, test, vi } from "vitest";

const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import { saveSettingsAction, sendTestMailAction } from "./actions";

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
  test("sends the feedback address trimmed and the mail language, under the API's keys", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await saveSettingsAction(
      {},
      form({ name: "USM", contact: "", feedback: "  https://forms.gle/beta  ", mailLanguage: "ru" }),
    );

    expect(serverRequest).toHaveBeenCalledWith("/settings", {
      method: "PUT",
      body: {
        values: {
          "installation.name": "USM",
          "installation.contact_email": "",
          "installation.feedback_url": "https://forms.gle/beta",
          "installation.mail_language": "ru",
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

describe("sendTestMailAction", () => {
  test("asks the API to send the test message and says to whom it went", async () => {
    serverRequest.mockResolvedValueOnce({ to: "root@usm.md" });

    const state = await sendTestMailAction();

    expect(serverRequest).toHaveBeenCalledWith("/settings/mail-test", { method: "POST" });
    expect(state).toEqual({ sentTo: "root@usm.md" });
  });

  test("passes a refusal through by its code, with the relay's reply", async () => {
    serverRequest.mockRejectedValueOnce(
      new ApiError("mail_relay_failed", 502, "no", "req-1", "535 5.7.139 Authentication unsuccessful"),
    );

    const state = await sendTestMailAction();

    expect(state).toEqual({ code: "mail_relay_failed", subject: "535 5.7.139 Authentication unsuccessful" });
  });

  test("passes a refusal with no reply through by its code alone", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("mail_off", 409, "no"));

    expect(await sendTestMailAction()).toEqual({ code: "mail_off" });
  });

  test("reads a server it could not reach as unreachable", async () => {
    serverRequest.mockRejectedValueOnce(new TypeError("fetch failed"));

    expect(await sendTestMailAction()).toEqual({ code: "unreachable" });
  });

  // A changed address shows on the next read; nothing else on the screen moves.
  test("revalidates nothing", async () => {
    serverRequest.mockResolvedValueOnce({ to: "root@usm.md" });

    await sendTestMailAction();

    expect(revalidatePath).not.toHaveBeenCalled();
  });
});
