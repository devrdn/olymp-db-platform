import { Band } from "@/components/layout/band";
import { registrationListSchema } from "@/lib/api/registrations";
import { readPage } from "@/lib/api/page-read";
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
  const [locale, dict, { registrations }] = await Promise.all([
    activeLocale(),
    activeDictionary(),
    readPage("/registrations", (payload) => registrationListSchema.parse(payload), { here: "/registrations" }),
  ]);

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
