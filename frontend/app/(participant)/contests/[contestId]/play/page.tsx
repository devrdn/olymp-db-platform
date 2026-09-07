import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { StoryText } from "@/components/product/story-text";
import { ApiError } from "@/lib/api/client";
import { contestListSchema, type ContestSummary } from "@/lib/api/contests";
import { playQuestionListSchema, playStorySchema } from "@/lib/api/play";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { formatMoment } from "@/lib/format/datetime";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Locale } from "@/lib/i18n/config";

import { Console } from "./console";
import { PlayHeader } from "./play-header";
import { QuestionsPanel, type QuestionEntry } from "./questions-panel";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.participant.console.heading };
}

/**
 * Error codes that mean this participant may not use this screen at all
 * right now, regardless of which of the two requests below surfaces one
 * first (finding 2). Every one of them comes out of the same admission gate
 * both `/play/story` and `/play/questions` share (`ParticipantHandler.admit`
 * on the Go side: `AdmitRead`, then `Access`) — a contest this participant's
 * own deadline has passed for, an address that stopped being allowed, an
 * account removed from the roster, or simply asking faster than this
 * installation allows. None of those is about the story or the questions in
 * particular: whichever request answers with one, the console beside them
 * would be refused for the exact same reason, so there is nothing left on
 * this screen worth keeping — it is shown the "not available" page below,
 * the same way `contest_finished` and `contest_not_running` (the two this
 * screen originally handled) already were.
 *
 * `story_not_found` is deliberately not in this set: it is a fact about the
 * story alone (`Reader.Story` refuses this way the instant a contest's story
 * has no translation for the negotiated language), and the questions and the
 * console are unaffected by it — see where it is handled below for why it
 * gets its own, narrower treatment instead.
 */
const SCREEN_UNAVAILABLE_CODES = new Set([
  "contest_finished",
  "contest_not_running",
  "not_a_participant",
  "address_not_allowed",
  "query_too_often",
  "question_not_found",
]);

/**
 * Where a participant works: the story, the questions, and the console that
 * used to be alone on this route, now beside them rather than a screen away.
 *
 * The contest is read from the participant's own enrolled listing rather than
 * from the contest endpoint, which is behind a staff permission — the same
 * reasoning the console's own version of this page already carried.
 *
 * What is read next depends on what that listing says the contest is doing.
 * `running` is the only status the participant-scoped `/play/*` endpoints
 * ever admit (participant_handler.go's own doc: Access requires the contest
 * running), so every other status is answered here rather than by sending a
 * request that can only be refused: `published` opens a waiting room that
 * holds an events connection and refreshes itself the instant the contest
 * starts, and anything else — draft, finished, archived — is a plain
 * "not available" with no connection at all, because none of those will ever
 * become `running` while this screen stays open.
 *
 * Even a `running` contest can still refuse this exact participant — their
 * own deadline may already be behind them while the contest keeps going for
 * everyone else (individual timing), the address they are on may no longer
 * be allowed, or they may simply be asking faster than this installation's
 * own rate allows — and every one of those is shown the same way, from the
 * same dictionary, rather than treated as a page that failed to load
 * (SCREEN_UNAVAILABLE_CODES's own doc, finding 2). The one exception is the
 * story missing a translation: that is a fact about the story alone, and
 * losing it must not lose the questions or the console beside it.
 */
export default async function PlayPage({ params }: PageProps<"/contests/[contestId]/play">) {
  const { contestId } = await params;
  const [locale, dict] = await Promise.all([activeLocale(), activeDictionary()]);
  const errors = dict.errors as Record<string, string>;

  const search = new URLSearchParams({ scope: "participant", enrolled: "true", lang: locale });
  const payload = await serverRequest(`/contests?${search}`).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, `/contests/${contestId}/play`);
    if (target) redirect(target);
    throw error;
  });

  const contest = contestListSchema.parse(payload).items.find((item) => item.id === contestId);
  if (!contest) redirect("/my");

  if (contest.status === "published") {
    return (
      <Band fill>
        <WaitingRoom contest={contest} locale={locale} dict={dict} />
      </Band>
    );
  }

  if (contest.status !== "running") {
    // draft, finished or archived: none of these ever becomes `running`
    // while this screen stays open, so there is nothing to wait for and
    // nothing worth opening a connection over.
    return <UnavailablePage title={contest.title} body={dict.participant.play.unavailable.body} />;
  }

  // Fetched together, and answered independently (finding 2): the two
  // requests share the same admission gate, so a refusal that is really
  // about this participant's own access to the contest (see
  // SCREEN_UNAVAILABLE_CODES) means the same thing regardless of which
  // settles first. But a refusal that is only about the story itself — it
  // has no translation for this language — has nothing to do with whether
  // the questions list or the console still work, so `Promise.all` (which
  // would fail the whole page on either rejecting) is deliberately not used
  // here; `allSettled` lets each answer be read on its own.
  const [storyResult, questionsResult] = await Promise.allSettled([
    serverRequest(`/contests/${contestId}/play/story?lang=${locale}`),
    serverRequest(`/contests/${contestId}/play/questions?lang=${locale}`),
  ]);

  if (questionsResult.status === "rejected") {
    const error = questionsResult.reason;
    if (error instanceof ApiError && SCREEN_UNAVAILABLE_CODES.has(error.code)) {
      return <UnavailablePage title={contest.title} body={errors[error.code]} />;
    }
    throw error;
  }
  const questions = playQuestionListSchema.parse(questionsResult.value).items;

  // The story: read on success, or reduced to a shown reason on the one
  // refusal that is about the story alone. Anything else — including a
  // SCREEN_UNAVAILABLE_CODES refusal reaching this request instead of the
  // other one — is answered the same way the questions list's own refusal
  // above is, since it means the same thing no matter which request it
  // arrived on.
  let storyBody: string | null = null;
  let storyUnavailable: string | null = null;
  if (storyResult.status === "fulfilled") {
    storyBody = playStorySchema.parse(storyResult.value).bodyMd;
  } else {
    const error = storyResult.reason;
    if (error instanceof ApiError && SCREEN_UNAVAILABLE_CODES.has(error.code)) {
      return <UnavailablePage title={contest.title} body={errors[error.code]} />;
    }
    if (error instanceof ApiError && error.code === "story_not_found") {
      storyUnavailable = errors.story_not_found;
    } else {
      throw error;
    }
  }

  const t = dict.participant.play;

  // Rendered here, once, on the server: `StoryText` runs `react-markdown`, a
  // real parser that costs nothing on this side of the wire and tens of
  // kilobytes gzipped on the other. A question's wording is fixed the moment
  // this page is built, so there is no reason to ship that parser to the
  // browser just so it can do the same parsing again — QuestionsPanel takes
  // the result, never the raw Markdown (its own doc explains the rest).
  //
  // The display number is carried alongside as `index` rather than
  // prepended to the Markdown before it is parsed (finding 6): a question
  // whose own wording opens with a heading, a list or a fenced block had
  // that block broken by whatever text this page concatenated in front of
  // it. QuestionsPanel renders the two as siblings instead.
  const questionEntries: QuestionEntry[] = questions.map((question, index) => ({
    question,
    index: index + 1,
    body: <StoryText key={question.id} markdown={question.bodyMd} className="max-w-none font-sans text-body" />,
  }));

  return (
    <Band fill>
      <div className="flex flex-col gap-6">
        <PlayHeader contestId={contestId} title={contest.title} waitingForStart={false} dict={dict} />

        <div className="grid gap-8 narrow:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] narrow:items-start">
          <div className="flex min-w-0 flex-col gap-10">
            <section className="flex flex-col gap-3">
              <h2 className="font-mono text-label text-ink-3 uppercase">{t.story.heading}</h2>
              {storyBody !== null ? <StoryText markdown={storyBody} /> : <p className="text-body text-ink-2">{storyUnavailable}</p>}
            </section>

            <section className="flex flex-col gap-4 border-t border-line pt-6">
              <h2 className="font-mono text-label text-ink-3 uppercase">{t.questions.heading}</h2>
              <QuestionsPanel contestId={contestId} items={questionEntries} dict={dict} />
            </section>
          </div>

          <section className="flex min-w-0 flex-col gap-3 narrow:border-l narrow:border-line narrow:pl-8">
            <h2 className="font-mono text-label text-ink-3 uppercase">{dict.participant.console.heading}</h2>
            <Console contestId={contestId} dict={dict} />
          </section>
        </div>
      </div>
    </Band>
  );
}

/** The plain "nothing to show" screen every non-running status and every whole-screen refusal (SCREEN_UNAVAILABLE_CODES) reduces to. */
function UnavailablePage({ title, body }: { title: string; body: string }) {
  return (
    <Band fill>
      <div className="flex flex-col gap-4">
        <h1 className="text-h2 text-ink">{title}</h1>
        <p className="max-w-body text-body text-ink">{body}</p>
      </div>
    </Band>
  );
}

/**
 * The room a participant who arrived early sits in.
 *
 * It holds the one events connection this screen has for a contest that has
 * not started, and asks the server for the real page — story, questions,
 * console — the instant `contest_started` arrives (`PlayHeader`'s own doc):
 * nothing here polls, and nothing here guesses.
 */
function WaitingRoom({
  contest,
  locale,
  dict,
}: {
  contest: ContestSummary;
  locale: Locale;
  dict: Dictionary;
}) {
  const t = dict.participant.play.waiting;
  // formatMoment (finding 4) resolves the installation's own timezone rather
  // than the runtime's: this is a Server Component, and without an explicit
  // zone `Intl.DateTimeFormat` reads whatever zone the server process
  // happens to run in — UTC inside a container — while the browser that
  // hydrates this same markup reads its own. A contest starting at noon
  // local time would show as nine in the morning here and noon on the
  // client, a mismatch the datetime helper exists precisely to rule out.
  const startsAt = contest.startsAt ? formatMoment(contest.startsAt, { locale }) : undefined;

  return (
    <div className="flex flex-col gap-6">
      <PlayHeader contestId={contest.id} title={contest.title} waitingForStart dict={dict} />
      <div className="flex max-w-body flex-col gap-2">
        <p className="text-body text-ink">{t.body}</p>
        {startsAt ? <p className="text-small text-ink-2">{t.startsAt.replace("{time}", startsAt)}</p> : null}
      </div>
    </div>
  );
}
