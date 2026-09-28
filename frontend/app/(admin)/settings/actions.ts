"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { serverRequest } from "@/lib/api/server";
import { IMAGE_KINDS, SETTING_KEYS } from "@/lib/api/settings";

export type SettingsState = { code?: string; saved?: boolean };

/**
 * Saving what the installation calls itself.
 *
 * A Server Action, so the form works with JavaScript off and the API's origin
 * never reaches the page. The values are sent under the API's own keys, which
 * live in one place: the day a setting is added, the table, the catalogue and
 * this form each gain a line, and nothing has to be kept in step by memory.
 */
export async function saveSettingsAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const values = {
    [SETTING_KEYS.name]: String(form.get("name") ?? "").trim(),
    [SETTING_KEYS.contact]: String(form.get("contact") ?? "").trim(),
  };

  const failure = await serverRequest("/settings", { method: "PUT", body: { values } }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    return { code: failureCode(failure) };
  }

  // The name is in the bar of every screen, so the whole tree is stale, not
  // just this page.
  revalidatePath("/", "layout");
  return { saved: true };
}

/**
 * Replacing one of the installation's marks.
 *
 * The bytes go straight through: what an upload says about itself is the
 * uploader's claim, and the API decides by reading them. Nothing here inspects
 * the file, because a check on this side would be a second opinion that can be
 * skipped by not using this form.
 */
export async function uploadImageAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const kind = String(form.get("kind") ?? "");
  if (!(IMAGE_KINDS as readonly string[]).includes(kind)) return { code: "invalid_request" };

  const file = form.get("file");
  if (!(file instanceof File) || file.size === 0) return { code: "invalid_request" };

  const failure = await serverRequest(`/settings/images/${kind}`, {
    method: "PUT",
    rawBody: await file.arrayBuffer(),
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    return { code: failureCode(failure) };
  }

  revalidatePath("/", "layout");
  return { saved: true };
}

export async function removeImageAction(
  _previous: SettingsState,
  form: FormData,
): Promise<SettingsState> {
  const kind = String(form.get("kind") ?? "");
  if (!(IMAGE_KINDS as readonly string[]).includes(kind)) return { code: "invalid_request" };

  const failure = await serverRequest(`/settings/images/${kind}`, { method: "DELETE" }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    return { code: failureCode(failure) };
  }

  revalidatePath("/", "layout");
  return { saved: true };
}
