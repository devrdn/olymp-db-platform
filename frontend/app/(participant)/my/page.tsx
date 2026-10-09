import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { contestListSchema } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { ParticipantRegister } from "./participant-register";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.participant.mine.heading };
}

/**
 * Where a participant lands after signing in: the contests they are enrolled
 * in (`/open` is the catalogue). `scope=participant` is explicit because an
 * account holding staff permissions too would otherwise get the contests it
 * manages.
 */
export default async function MyContestsPage() {
  const [locale, dict] = await Promise.all([activeLocale(), activeDictionary()]);

  // `enrolled=true` keeps the open contests out: this screen answers "when
  // does mine start", asked under a timer on the day.
  const search = new URLSearchParams({ scope: "participant", enrolled: "true", lang: locale });

  // The proxy only saw that a cookie exists. A dead session goes back to
  // sign-in and a one-time password to the password screen; a retry could
  // fix neither.
  const payload = await serverRequest(`/contests?${search}`).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, "/my");
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
        heading={dict.participant.mine.heading}
        countLabel={dict.participant.mine.countLabel}
        empty={{
          title: dict.participant.mine.empty.title,
          body: dict.participant.mine.empty.body,
          // The empty state names its next step (SPEC.md §2, principle 4).
          action: { label: dict.participant.mine.empty.action, href: "/open" },
        }}
      />
    </Band>
  );
}
