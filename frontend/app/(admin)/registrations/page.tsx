import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { registrationListSchema } from "@/lib/api/registrations";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { RegistrationDesk } from "./desk";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.registration.desk.heading };
}

/**
 * The event's registrations for the organisers at the entrance. The API
 * returns the whole bounded list, and the desk searches it in the browser.
 */
export default async function RegistrationsPage() {
  const [locale, dict, payload] = await Promise.all([
    activeLocale(),
    activeDictionary(),
    serverRequest("/registrations").catch((error: unknown) => {
      const target = authRecoveryRedirect(error, "/registrations");
      if (target) redirect(target);
      throw error;
    }),
  ]);

  const { registrations } = registrationListSchema.parse(payload);

  return (
    <Band fill className="py-12">
      <RegistrationDesk
        registrations={registrations}
        t={dict.registration.desk}
        errors={dict.errors}
        locale={locale}
      />
    </Band>
  );
}
