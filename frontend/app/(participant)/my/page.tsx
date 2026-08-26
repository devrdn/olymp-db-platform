import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { contestListSchema } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { ParticipantRegister } from "./participant-register";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.participant.heading };
}

/**
 * Where a participant lands after signing in.
 *
 * A Server Component: the data is fetched where the session already is, so no
 * token reaches the browser and the first paint carries the rows.
 *
 * `scope=participant` is passed explicitly rather than relied on. The API
 * scopes the listing by what the caller may see, and for an account holding no
 * staff permission that already means "mine and the open ones" — but an
 * account that holds both roles, which the teaching assistant running a
 * contest and sitting another one does, would otherwise get the contests they
 * manage on the screen that promises the ones they take part in.
 */
export default async function MyContestsPage() {
  const [locale, dict] = await Promise.all([activeLocale(), activeDictionary()]);

  const search = new URLSearchParams({ scope: "participant", lang: locale });

  // The proxy could only see that a session cookie exists; what it is worth is
  // this answer. A dead session goes back to the form and an account still on
  // its one-time password goes to the password screen — neither is a failure a
  // retry could fix, which is what the error boundary would offer.
  const payload = await serverRequest(`/contests?${search}`).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, "/my");
    if (target) redirect(target);
    throw error;
  });

  const { items, total } = contestListSchema.parse(payload);

  return (
    <Band fill className="py-12">
      <ParticipantRegister contests={items} total={total} dict={dict} locale={locale} />
    </Band>
  );
}
