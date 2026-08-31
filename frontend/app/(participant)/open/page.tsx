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
 * The catalogue: everything a participant may see, whether or not they are on
 * it.
 *
 * Deliberately not "only what I have not joined". Filtering out the contests
 * somebody is already enrolled in would answer "what is there" incompletely,
 * and a student who cannot find a familiar name concludes their registration
 * was lost. They are listed and marked instead — which is what the `enrolled`
 * flag on each row is for.
 *
 * The address is `/open` rather than `/contests`: route groups do not appear
 * in URLs, so `/contests` is the author's register. A participant who reached
 * it would meet that register scoped to contests they manage, which for them
 * is empty — an accurate answer to a question they never asked.
 */
export default async function OpenContestsPage() {
  const [locale, dict] = await Promise.all([activeLocale(), activeDictionary()]);

  // No `enrolled` narrowing: this is the whole visible set. The scope still
  // decides what may be seen at all, and a draft or somebody else's
  // invitation-only contest is not in it.
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
        // No next step: this screen is the next step. A link back to itself
        // would be the empty state offering the reader where they already are.
        empty={dict.participant.open.empty}
      />
    </Band>
  );
}
