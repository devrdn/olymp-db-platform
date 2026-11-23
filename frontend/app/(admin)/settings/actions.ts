"use server";

import { z } from "zod";

import { forgetBranding } from "@/lib/api/branding";
import { serverAction, serverRequest, type ActionState } from "@/lib/api/server";
import { IMAGE_KINDS, SETTING_KEYS, feedbackHref } from "@/lib/api/settings-terms";

export type SettingsState = ActionState;

/**
 * Saves one installation setting. The name, the marks and the archive's entry
 * are in every screen's bar, so the branding cache goes and every layout is
 * revalidated.
 */
async function saveInstallation(path: string, init: { method: string; body?: unknown; rawBody?: ArrayBuffer }) {
  const saved = await serverAction(() => serverRequest(path, init), { revalidate: [{ layout: "/" }] });
  if (saved.ok) forgetBranding();
  return saved;
}

/** After a switch is pressed: the same two answers as a save. */
export type SwitchState = SettingsState;

/** Saves the installation's settings under the API's own keys. */
export async function saveSettingsAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const feedback = String(form.get("feedback") ?? "").trim();
  if (feedback !== "" && feedbackHref(feedback) === null) return { code: "invalid_feedback_url" };

  const values = {
    [SETTING_KEYS.name]: String(form.get("name") ?? "").trim(),
    [SETTING_KEYS.contact]: String(form.get("contact") ?? "").trim(),
    [SETTING_KEYS.feedback]: feedback,
    [SETTING_KEYS.mailLanguage]: String(form.get("mailLanguage") ?? ""),
  };

  return saveInstallation("/settings", { method: "PUT", body: { values } });
}

/**
 * Replaces one of the installation's marks. The bytes are passed through
 * uninspected; the API decides by reading them.
 */
export async function uploadImageAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const kind = String(form.get("kind") ?? "");
  if (!(IMAGE_KINDS as readonly string[]).includes(kind)) return { code: "invalid_request" };

  const file = form.get("file");
  if (!(file instanceof File) || file.size === 0) return { code: "invalid_request" };

  return saveInstallation(`/settings/images/${kind}`, {
    method: "PUT",
    rawBody: await file.arrayBuffer(),
  });
}

export async function removeImageAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const kind = String(form.get("kind") ?? "");
  if (!(IMAGE_KINDS as readonly string[]).includes(kind)) return { code: "invalid_request" };

  return saveInstallation(`/settings/images/${kind}`, { method: "DELETE" });
}

/** Switches the contest archive on or off for the whole installation. */
export async function switchArchiveAction(_previous: SwitchState, form: FormData): Promise<SwitchState> {
  const enabled = String(form.get("enabled") ?? "");
  if (enabled !== "true" && enabled !== "false") return { code: "invalid_request" };

  return saveInstallation("/settings/archive", {
    method: "PUT",
    body: { enabled: enabled === "true" },
  });
}

/** After a test message: the address it went to, or why it did not, with the relay's reply when there was one. */
export type MailTestState = ActionState & { sentTo?: string; subject?: string };

const mailTestResultSchema = z.object({ to: z.string() });

/** Sends the caller the test message; the API picks the address, the caller's own. */
export async function sendTestMailAction(): Promise<MailTestState> {
  const send = async () => mailTestResultSchema.parse(await serverRequest("/settings/mail-test", { method: "POST" }));
  return serverAction(send, {
    success: ({ to }) => ({ ok: true, sentTo: to }),
    // The relay's reply, when there was one, is what the administrator needs to see.
    failure: ({ code, subject }) => (subject ? { code, subject } : { code }),
  });
}
