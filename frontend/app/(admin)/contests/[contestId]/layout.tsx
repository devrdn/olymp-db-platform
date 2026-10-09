import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { Tag } from "@/components/ui/tag";
import { questionListSchema, untranslated } from "@/lib/api/content";
import { contentEditable, publishCheckSchema, titleIn } from "@/lib/api/contests";
import { PUBLISH_PROBLEMS } from "@/lib/api/publish-gate";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { ContestCrumbs } from "./contest-crumbs";
import { ContestNav, type NavGroup } from "./contest-nav";
import { loadContest, loadContestResource } from "./contest";
import { TitleEditor } from "./title-editor";
import { CONTEST_STATUS_TONE } from "@/lib/api/contests-terms";

export async function generateMetadata(props: LayoutProps<"/contests/[contestId]">) {
  const [{ contestId }, locale] = await Promise.all([props.params, activeLocale()]);
  const contest = await loadContest(contestId);

  return { title: titleIn(contest, locale) };
}

/**
 * The frame of one contest's screens: the contest, its state, what it still
 * owes, and the sections. Loaded here so the heading and the page come from one
 * deduplicated pass and describe the same revision. The publish gate marks each
 * section that holds blocking work.
 */
export default async function ContestLayout(props: LayoutProps<"/contests/[contestId]">) {
  const [{ contestId }, locale, dict] = await Promise.all([
    props.params,
    activeLocale(),
    activeDictionary(),
  ]);

  // Independent requests, run concurrently; the overview's own gate request is
  // memoised into this one.
  const [contest, check] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/publish-check", (payload) =>
      publishCheckSchema.parse(payload),
    ).catch(() => null),
  ]);
  const t = dict.workspace;
  const base = `/contests/${contest.id}`;
  // The server decides contest.monitor with the monitoring routes' authoriser.
  const monitor = contest.mayMonitor;

  const groups = await navigation(
    contestId,
    base,
    contest.languages.map((l) => l.code),
    dict,
    check,
    monitor,
  );

  return (
    <>
      <Band className="gap-6 pt-9 pb-7">
        <ContestCrumbs
          label={t.breadcrumb}
          register={{ href: "/contests", label: dict.contests.heading }}
          contestHref={base}
          title={titleIn(contest, locale) || t.untitled}
          sections={{
            story: t.tabs.story,
            questions: t.tabs.questions,
            people: t.tabs.people,
            settings: t.tabs.settings,
            game: t.tabs.game,
            databases: t.tabs.databases,
            standings: t.tabs.leaderboard,
            monitor: t.tabs.monitor,
          }}
        />

        <div className="flex min-w-0 flex-col gap-3">
          <div className="flex flex-wrap items-baseline gap-3">
            <h1 className="max-w-head text-h2 text-balance text-ink">
              {/* A draft may have no title yet. */}
              {titleIn(contest, locale) || <span className="text-ink-3">{t.untitled}</span>}
            </h1>

            {/* Edited here, where every screen of the contest shows it (see
               `title-editor.tsx`). */}
            <TitleEditor
              contest={contest}
              editable={contentEditable(contest.status)}
              dict={dict}
            />
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{dict.contests.status[contest.status]}</Tag>
            <span className="font-mono text-data text-ink-3">
              {dict.contests.mode[contest.questionMode]}
            </span>
            <span aria-hidden className="h-3 w-px bg-line-2" />
            <span className="font-mono text-data text-ink-2">
              <ContestWindow
                startsAt={contest.startsAt}
                endsAt={contest.endsAt}
                locale={locale}
                unscheduled={dict.contests.unscheduled}
                until={dict.contests.until}
              />
            </span>
          </div>
        </div>
      </Band>

      {/* The rule is a 1px grid track, so it runs the full height of the taller
         side and never collapses. */}
      <Band fill className="grid content-start gap-x-9 gap-y-8 py-9 narrow:grid-cols-[13rem_1px_minmax(0,1fr)] narrow:content-stretch">
        <ContestNav groups={groups} />
        <div aria-hidden className="hidden bg-line narrow:block" />
        <div className="flex min-w-0 flex-col">{props.children}</div>
      </Band>
    </>
  );
}

/**
 * The sections with their notes from the publish gate, which answers 200 with
 * every problem. `check` is fetched by the caller concurrently with the
 * contest. An unreachable gate leaves the notes empty rather than failing.
 */
async function navigation(
  contestId: string,
  base: string,
  languages: string[],
  dict: Awaited<ReturnType<typeof activeDictionary>>,
  check: ReturnType<typeof publishCheckSchema.parse> | null,
  monitor: boolean,
): Promise<NavGroup[]> {
  const t = dict.workspace;

  const problems = check?.problems ?? [];
  const has = (code: string) => problems.some((p) => p.code === code);

  const storyNote = has(PUBLISH_PROBLEMS.noStory)
    ? t.notes.none
    : countLanguages(problems, PUBLISH_PROBLEMS.missingStoryTranslation, languages);

  const questionNote = has(PUBLISH_PROBLEMS.noQuestions)
    ? t.notes.none
    : await questionNoteFor(contestId, languages);

  return [
    { items: [{ href: base, label: t.tabs.overview, exact: true }] },
    {
      label: t.groups.content,
      items: [
        { href: `${base}/story`, label: t.tabs.story, note: storyNote },
        { href: `${base}/questions`, label: t.tabs.questions, note: questionNote },
        // Content: the game sits behind the same edit gate as the story and the
        // questions.
        { href: `${base}/game`, label: t.tabs.game },
      ],
    },
    {
      label: t.groups.setup,
      items: [
        { href: `${base}/people`, label: t.tabs.people },
        { href: `${base}/settings`, label: t.tabs.settings },
        // Read while the contest runs, so it follows people and settings. No
        // note: notes come from the publish gate, and database state never
        // blocks publishing.
        { href: `${base}/databases`, label: t.tabs.databases },
        // Read during and after the contest.
        { href: `${base}/standings`, label: t.tabs.leaderboard },
        // Only for holders of contest.monitor.
        ...(monitor ? [{ href: `${base}/monitor`, label: t.tabs.monitor }] : []),
      ],
    },
  ];
}

/** How many declared languages a per-language problem names. */
function countLanguages(
  problems: { code: string; lang?: string }[],
  code: string,
  languages: string[],
): string | undefined {
  const named = new Set(
    problems.filter((p) => p.code === code && p.lang).map((p) => p.lang as string),
  );
  const missing = languages.filter((lang) => named.has(lang)).length;

  return missing > 0 ? `${missing}` : undefined;
}

/**
 * Counts incomplete questions rather than the gate's per-question-per-language
 * entries, so the number matches the list.
 */
async function questionNoteFor(
  contestId: string,
  languages: string[],
): Promise<string | undefined> {
  const list = await loadContestResource(contestId, "/questions", (payload) =>
    questionListSchema.parse(payload),
  ).catch(() => null);

  if (!list) return undefined;

  const incomplete = list.items.filter(
    (question) => untranslated(question, languages).length > 0,
  ).length;

  return incomplete > 0 ? `${incomplete}` : undefined;
}
