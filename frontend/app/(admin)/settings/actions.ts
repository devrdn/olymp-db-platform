"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { serverRequest } from "@/lib/api/server";
import { IMAGE_KINDS, SETTING_KEYS } from "@/lib/api/settings";

export type SettingsState = { code?: string; saved?: boolean };

/** Saves the installation's settings under the API's own keys. */
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

  // The name is in every screen's bar.
  revalidatePath("/", "layout");
  return { saved: true };
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
