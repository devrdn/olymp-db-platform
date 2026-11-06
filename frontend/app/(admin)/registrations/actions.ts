"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { registrationSchema } from "@/lib/api/registrations";
import type { Registration } from "@/lib/api/registrations-terms";
import { serverRequest } from "@/lib/api/server";

export type CheckInResult = { registration?: Registration; code?: string };

/** Marks a registered guest as arrived, or takes the mark back. */
export async function checkInAction(id: string, checkedIn: boolean): Promise<CheckInResult> {
  // Both come from the browser and can be forged.
  if (!isId(id)) return { code: "registration_not_found" };
  if (typeof checkedIn !== "boolean") return { code: "invalid_request" };

  let registration: Registration;
  try {
    const payload = await serverRequest(`/registrations/${id}/check-in`, {
      method: "PUT",
      body: { checked_in: checkedIn },
    });
    registration = registrationSchema.parse(payload);
  } catch (error) {
    return { code: failureCode(error) };
  }

  revalidatePath("/registrations");
  return { registration };
}
