import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { contestListSchema } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { Console } from "./console";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.participant.console.heading };
}

/**
 * Where a participant works on their own copy of the contest's database.
 *
 * The contest is read from the participant's own enrolled listing rather than
 * from the contest endpoint, which is behind a staff permission: a competitor
 * has no right to read a contest, they have a registration in one. Finding it
 * absent from that listing is the same fact as not being in the contest, and
 * the answer is the same — back to where their contests are.
 *
 * The story and the questions are not here yet: reading them needs endpoints
 * scoped to a participant, which the constructor's are not.
 */
export default async function PlayPage({ params }: PageProps<"/contests/[contestId]/play">) {
  const { contestId } = await params;
  const [locale, dict] = await Promise.all([activeLocale(), activeDictionary()]);

  const search = new URLSearchParams({ scope: "participant", enrolled: "true", lang: locale });
  const payload = await serverRequest(`/contests?${search}`).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, `/contests/${contestId}/play`);
    if (target) redirect(target);
    throw error;
  });

  const contest = contestListSchema.parse(payload).items.find((item) => item.id === contestId);
  if (!contest) redirect("/my");

  return (
    <Band fill>
      <div className="flex flex-col gap-6">
        <header className="flex flex-col gap-1">
          <h1 className="text-h2 text-ink">{contest.title}</h1>
          <p className="text-body text-ink-2">{dict.participant.console.lede}</p>
        </header>

        <Console contestId={contestId} dict={dict} />
      </div>
    </Band>
  );
}
