"use server";

import { failureCode } from "@/lib/api/client";
import { registrationOutcomeSchema, type RegistrationOutcome } from "@/lib/api/registrations";
import { serverRequest } from "@/lib/api/server";

export type RegisterValues = { firstName: string; lastName: string; email: string };

/**
 * Either the outcome, or a code with what was typed, since React resets an
 * action's form and the visitor should not type it all again.
 */
export type RegisterState = {
  code?: string;
  outcome?: RegistrationOutcome;
  values?: RegisterValues;
};

/**
 * Registers a visitor for the event. The API applies the real bounds; the
 * empty fields refused here only save a round trip.
 */
export async function registerAction(_previous: RegisterState, form: FormData): Promise<RegisterState> {
  const text = (name: string) => String(form.get(name) ?? "").trim();
  const values: RegisterValues = {
    firstName: text("first_name"),
    lastName: text("last_name"),
    email: text("email"),
  };

  if (values.firstName === "" || values.lastName === "") {
    return { code: "registration_invalid_name", values };
  }
  if (values.email === "") return { code: "registration_invalid_email", values };

  try {
    const payload = await serverRequest("/public/registrations", {
      method: "POST",
      body: { first_name: values.firstName, last_name: values.lastName, email: values.email },
    });
    return { outcome: registrationOutcomeSchema.parse(payload) };
  } catch (error) {
    return { code: failureCode(error), values };
  }
}
