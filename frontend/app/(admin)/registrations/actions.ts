"use server";

import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { registrationSchema } from "@/lib/api/registrations";
import type { Registration } from "@/lib/api/registrations-terms";
import { serverRequest } from "@/lib/api/server";

export type CheckInResult = { registration?: Registration; code?: string };

/**
 * Marks a registered guest as arrived, or takes the mark back. It does not
 * revalidate the page: the desk shows the returned row, and a re-render would
 * carry the whole list in every tap's answer.
 */
export async function checkInAction(id: string, checkedIn: boolean): Promise<CheckInResult> {
  // Both come from the browser and can be forged.
  if (!isId(id)) return { code: "registration_not_found" };
  if (typeof checkedIn !== "boolean") return { code: "invalid_request" };

  try {
    const payload = await serverRequest(`/registrations/${id}/check-in`, {
      method: "PUT",
      body: { checked_in: checkedIn },
    });
    return { registration: registrationSchema.parse(payload) };
  } catch (error) {
    return { code: failureCode(error) };
  }
}
