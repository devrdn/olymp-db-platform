import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { Tag } from "@/components/ui/tag";
import { questionListSchema, untranslated } from "@/lib/api/content";
import { contentEditable, publishCheckSchema, titleIn, type ContestStatus } from "@/lib/api/contests";
import { PUBLISH_PROBLEMS } from "@/lib/api/publish-gate";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { ContestCrumbs } from "./contest-crumbs";
import { ContestNav, type NavGroup } from "./contest-nav";
import { loadContest, loadContestResource } from "./contest";
import { TitleEditor } from "./title-editor";

/** One tone per state, and the accent spent only on what is happening now. */
const STATUS_TONE: Record<ContestStatus, "live" | "good" | "mute"> = {
  draft: "mute",
  published: "good",
  running: "live",
  finished: "mute",
  archived: "mute",
};

export async function generateMetadata(props: LayoutProps<"/contests/[contestId]">) {
  const [{ contestId }, locale] = await Promise.all([props.params, activeLocale()]);
  const contest = await loadContest(contestId);

  return { title: titleIn(contest, locale) };
}

/**
 * The frame every screen of one contest wears: which contest, what state it is
 * in, what it still owes, and the way between its sections.
 *
 * The contest is loaded here rather than in each page. A layout and its page
 * render in the same pass with `fetch` deduplicated across them, so the
 * heading and the section below it are guaranteed to describe the same
 * revision — which two independent requests do not guarantee, and the symptom
 * of that is a title disagreeing with the badge beside it.
 *
 * The gate is loaded here too, and that is what the navigation column is for.
 * Publishing is blocked by work that lives in particular sections, and a gate
 * report on the overview makes an author read a list, translate each line into
 * a section, and navigate there. Marking the section itself skips all three
 * steps: the work is named where the work is done.
 *
 * Two bands. The header is one and the workspace is another, so the hatched
 * margins run unbroken down the page and a section can fill its own band
 * without inheriting padding meant for a title.
 */
export default async function ContestLayout(props: LayoutProps<"/contests/[contestId]">) {
  const [{ contestId }, locale, dict] = await Promise.all([
    props.params,
    activeLocale(),
    activeDictionary(),
  ]);

  // `contest` and the publish gate are independent requests — the gate needs
  // only the id, not anything the contest fetch returns — so they run
  // concurrently rather than the gate waiting out a round trip it never
  // needed to. The overview page (`page.tsx`) asks the same gate endpoint
  // again; `fetch`'s own request memoisation collapses that into the one
  // call already in flight from here, same pass, same request.
  const [contest, check] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/publish-check", (payload) =>
      publishCheckSchema.parse(payload),
    ).catch(() => null),
  ]);
  const t = dict.workspace;
  const base = `/contests/${contest.id}`;

  const groups = await navigation(contestId, base, contest.languages.map((l) => l.code), dict, check);

  return (
    <>
      <Band className="gap-6 pt-9 pb-7">
        {/* The trail replaces what was a lone "back to the register" link. It
            leads to the same place and answers the question that link did not:
            not only where one step out goes, but where the visitor is. */}
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
          }}
        />

        <div className="flex min-w-0 flex-col gap-3">
          <div className="flex flex-wrap items-baseline gap-3">
            <h1 className="max-w-head text-h2 text-balance text-ink">
              {/* A draft has no title until somebody writes one, and a blank
                  heading tells the author nothing about what they have open. */}
              {titleIn(contest, locale) || <span className="text-ink-3">{t.untitled}</span>}
            </h1>

            {/* The name, editable from the one place every screen of this
                contest already shows it — see `title-editor.tsx` for why it
                moved here from a settings panel. */}
            <TitleEditor
              contest={contest}
              editable={contentEditable(contest.status)}
              dict={dict}
            />
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <Tag tone={STATUS_TONE[contest.status]}>{dict.contests.status[contest.status]}</Tag>
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

      {/* The rule between the column and the work is a grid track, not a
          border on either side of it: a 1px track cannot be rounded off by a
          margin collapse and runs the full height of whichever side is taller. */}
      <Band fill className="grid content-start gap-x-9 gap-y-8 py-9 narrow:grid-cols-[13rem_1px_minmax(0,1fr)] narrow:content-stretch">
        <ContestNav groups={groups} />
        <div aria-hidden className="hidden bg-line narrow:block" />
        <div className="flex min-w-0 flex-col">{props.children}</div>
      </Band>
    </>
  );
}

/**
 * The sections, with what each one still owes.
 *
 * The notes come from the publish gate, which answers 200 even when publishing
 * is impossible and returns every problem at once. Read here, its flat list
 * becomes a mark against the section that owns the work.
 *
 * `check` is fetched by the caller, alongside the contest itself, rather than
 * by this function: the gate needs only the contest id, so waiting for the
 * contest to resolve first — which awaiting it in here would do, since this
 * is called after that await — is a round trip this owes nobody. A gate that
 * cannot be reached is not an error worth a screen: the sections are still
 * there and still work. The navigation simply says nothing, which is honest —
 * it does not know.
 */
async function navigation(
  contestId: string,
  base: string,
  languages: string[],
  dict: Awaited<ReturnType<typeof activeDictionary>>,
  check: ReturnType<typeof publishCheckSchema.parse> | null,
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
      ],
    },
    {
      label: t.groups.setup,
      items: [
        { href: `${base}/people`, label: t.tabs.people },
        { href: `${base}/settings`, label: t.tabs.settings },
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
 * What the question list owes, counted from the questions themselves.
 *
 * The gate reports a missing translation per question and language, which
 * would make the note a number an author cannot act on — "6" across three
 * questions and two languages is not six pieces of work. Counting questions
 * that are incomplete gives a figure that matches what the list shows.
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
