"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { serverRequest } from "@/lib/api/server";
import { PROVIDER_IDS } from "@/lib/auth/external";

export type SwitchState = { code?: string; saved?: boolean };

/** Switches a configured provider on or off; the API refuses one that is not configured. */
export async function switchProviderAction(_previous: SwitchState, form: FormData): Promise<SwitchState> {
  const provider = String(form.get("provider") ?? "");
  const enabled = String(form.get("enabled") ?? "");
  if (!(PROVIDER_IDS as readonly string[]).includes(provider)) return { code: "invalid_request" };
  if (enabled !== "true" && enabled !== "false") return { code: "invalid_request" };

  const failure = await serverRequest(`/integrations/${provider}`, {
    method: "PUT",
    body: { enabled: enabled === "true" },
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    return { code: failureCode(failure) };
  }

  revalidatePath("/integrations");
  return { saved: true };
}
