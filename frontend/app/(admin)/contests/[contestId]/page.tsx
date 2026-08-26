import { Band } from "@/components/layout/band";
import {
  contentEditable,
  defaultLanguage,
  NEXT_STATUSES,
  publishCheckSchema,
} from "@/lib/api/contests";
import { summarisePublishCheck } from "@/lib/api/publish-gate";
import { formatMoment } from "@/lib/format/datetime";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "./contest";
import { PublishGateReport } from "./publish-gate";
import { StatusActions } from "./status-actions";

/**
 * The contest at a glance: what state it is in, what stands between it and
 * publication, and what an author is still allowed to change.
 *
 * The gate is fetched here rather than only on the way out of a failed
 * publish. Being told what is left *before* pressing the button is the
 * difference between a checklist and a refusal, and the endpoint exists
 * precisely so the constructor can show the remaining work.
 */
export default async function ContestOverviewPage(props: PageProps<"/contests/[contestId]">) {
  const [{ contestId }, locale, dict] = await Promise.all([
    props.params,
    activeLocale(),
    activeDictionary(),
  ]);

  // Deduplicated against the layout's own call: same pass, same request.
  const contest = await loadContest(contestId);

  const check = await loadContestResource(contestId, "/publish-check", (payload) =>
    publishCheckSchema.parse(payload),
  );

  const languages = contest.languages.map((l) => l.code);
  const gate = check ? summarisePublishCheck(check, languages) : null;
  const t = dict.workspace;

  return (
    <Band fill className="gap-12 py-12">
      <section aria-labelledby="gate-heading" className="flex flex-col gap-6">
        <h2 id="gate-heading" className="text-h3 text-ink">
          {t.gate.heading}
        </h2>

        {gate && check ? (
          <PublishGateReport gate={gate} ready={check.ready} dict={dict} />
        ) : (
          <p className="text-body text-ink-3">{t.gate.unavailable}</p>
        )}

        <StatusActions
          contestId={contest.id}
          next={NEXT_STATUSES[contest.status]}
          blocked={check ? !check.ready : true}
          dict={dict}
        />
      </section>

      <section aria-labelledby="facts-heading" className="flex flex-col gap-6">
        <h2 id="facts-heading" className="text-h3 text-ink">
          {t.facts.heading}
        </h2>

        {/* A description list, because that is what this is: a term and its
            value, repeated. A grid of divs would say the same thing to a
            sighted reader and nothing at all to anyone else. */}
        <dl className="grid gap-x-10 gap-y-5 narrow:grid-cols-2">
          <Fact term={t.facts.format} value={dict.contests.mode[contest.questionMode]} />
          <Fact term={t.facts.timing} value={t.timing[contest.timing]} />
          <Fact
            term={t.facts.enrollment}
            value={dict.contests.enrollment[contest.enrollment]}
          />
          <Fact
            term={t.facts.duration}
            value={
              contest.durationMin
                ? t.facts.minutes.replace("{n}", String(contest.durationMin))
                : t.facts.wholeWindow
            }
          />
          <Fact
            term={t.facts.languages}
            value={
              languages.length > 0
                ? languages
                    .map((code) => (code === defaultLanguage(contest) ? `${code}*` : code))
                    .join(", ")
                : t.facts.none
            }
            mono
          />
          <Fact
            term={t.facts.network}
            value={
              contest.allowedCidrs.length > 0 ? contest.allowedCidrs.join(", ") : t.facts.anywhere
            }
            mono
          />
          <Fact
            term={t.facts.contentWindow}
            value={contentEditable(contest.status) ? t.facts.open : t.facts.frozen}
          />
          <Fact term={t.facts.updated} value={formatMoment(contest.updatedAt, { locale })} mono />
        </dl>
      </section>
    </Band>
  );
}

function Fact({ term, value, mono }: { term: string; value: string; mono?: boolean }) {
  return (
    <div className="flex flex-col gap-1 border-t border-line pt-3">
      <dt className="font-mono text-label text-ink-3 uppercase">{term}</dt>
      <dd className={mono ? "font-mono text-data text-ink" : "text-row text-ink"}>{value}</dd>
    </div>
  );
}
