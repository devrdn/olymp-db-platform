"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import { serverRequest } from "@/lib/api/server";
import { SETTING_KEYS } from "@/lib/api/settings";

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
    return { code: failure instanceof ApiError ? failure.code : "unreachable" };
  }

  // The name is in the bar of every screen, so the whole tree is stale, not
  // just this page.
  revalidatePath("/", "layout");
  return { saved: true };
}
