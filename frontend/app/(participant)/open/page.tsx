import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { contestListSchema } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { ParticipantRegister } from "../my/participant-register";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.participant.open.heading };
}

/**
 * The catalogue: every contest a participant may see, joined or not. Joined
 * ones are listed and marked (`enrolled`), since a familiar name missing
 * would look like a lost registration. At `/open` because route groups do
 * not appear in URLs, and `/contests` is the author's register.
 */
export default async function OpenContestsPage() {
  const [locale, dict] = await Promise.all([activeLocale(), activeDictionary()]);

  // No `enrolled` filter: the whole visible set. The scope still hides drafts
  // and other people's invitation-only contests.
  const search = new URLSearchParams({ scope: "participant", lang: locale });

  const payload = await serverRequest(`/contests?${search}`).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, "/open");
    if (target) redirect(target);
    throw error;
  });

  const { items, total } = contestListSchema.parse(payload);

  return (
    <Band fill className="py-12">
      <ParticipantRegister
        contests={items}
        total={total}
        dict={dict}
        locale={locale}
        heading={dict.participant.open.heading}
        countLabel={dict.participant.open.countLabel}
        // No action: this screen is already the next step.
        empty={dict.participant.open.empty}
      />
    </Band>
  );
}
