"use server";

import { serverAction, serverRequest, type ActionState } from "@/lib/api/server";
import { isProviderId } from "@/lib/auth/external";

export type SwitchState = ActionState;

/** Switches a configured provider on or off; the API refuses one that is not configured. */
export async function switchProviderAction(_previous: SwitchState, form: FormData): Promise<SwitchState> {
  const provider = String(form.get("provider") ?? "");
  const enabled = String(form.get("enabled") ?? "");
  if (!isProviderId(provider)) return { code: "invalid_request" };
  if (enabled !== "true" && enabled !== "false") return { code: "invalid_request" };

  return serverAction(
    () => serverRequest(`/integrations/${provider}`, { method: "PUT", body: { enabled: enabled === "true" } }),
    { revalidate: ["/integrations"] },
  );
}
