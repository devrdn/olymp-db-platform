import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { StoryText } from "@/components/product/story-text";
import { ApiError } from "@/lib/api/client";
import { contestListSchema, type ContestSummary } from "@/lib/api/contests";
import { playQuestionListSchema, playStorySchema } from "@/lib/api/play";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
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
 * everyone else (individual timing) — and that refusal is shown the same way,
 * from the same dictionary, rather than treated as a page that failed to load.
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
    return (
      <Band fill>
        <div className="flex flex-col gap-4">
          <h1 className="text-h2 text-ink">{contest.title}</h1>
          <p className="max-w-body text-body text-ink">{dict.participant.play.unavailable.body}</p>
        </div>
      </Band>
    );
  }

  let story;
  let questions;
  try {
    const [storyPayload, questionsPayload] = await Promise.all([
      serverRequest(`/contests/${contestId}/play/story?lang=${locale}`),
      serverRequest(`/contests/${contestId}/play/questions?lang=${locale}`),
    ]);
    story = playStorySchema.parse(storyPayload);
    questions = playQuestionListSchema.parse(questionsPayload).items;
  } catch (error: unknown) {
    // The contest is `running`, but this participant's own access to it is
    // not — their individual deadline has passed, or the scheduler has not
    // yet caught the contest up to its own end. Either way it is the same
    // fact a fresh events connection would also be refused for, so this is
    // shown the same way the other non-running statuses are, in the API's
    // own words rather than as a page that failed to load.
    if (
      error instanceof ApiError &&
      (error.code === "contest_finished" || error.code === "contest_not_running")
    ) {
      return (
        <Band fill>
          <div className="flex flex-col gap-4">
            <h1 className="text-h2 text-ink">{contest.title}</h1>
            <p className="max-w-body text-body text-ink">
              {(dict.errors as Record<string, string>)[error.code]}
            </p>
          </div>
        </Band>
      );
    }
    throw error;
  }

  const t = dict.participant.play;

  // Rendered here, once, on the server: `StoryText` runs `react-markdown`, a
  // real parser that costs nothing on this side of the wire and tens of
  // kilobytes gzipped on the other. A question's wording is fixed the moment
  // this page is built, so there is no reason to ship that parser to the
  // browser just so it can do the same parsing again — QuestionsPanel takes
  // the result, never the raw Markdown (its own doc explains the rest).
  const questionEntries: QuestionEntry[] = questions.map((question, index) => ({
    question,
    body: (
      <StoryText
        key={question.id}
        markdown={`**${index + 1}.** ${question.bodyMd}`}
        className="max-w-none font-sans text-body"
      />
    ),
  }));

  return (
    <Band fill>
      <div className="flex flex-col gap-6">
        <PlayHeader contestId={contestId} title={contest.title} waitingForStart={false} dict={dict} />

        <div className="grid gap-8 narrow:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] narrow:items-start">
          <div className="flex min-w-0 flex-col gap-10">
            <section className="flex flex-col gap-3">
              <h2 className="font-mono text-label text-ink-3 uppercase">{t.story.heading}</h2>
              <StoryText markdown={story.bodyMd} />
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
  const startsAt = contest.startsAt
    ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(
        new Date(contest.startsAt),
      )
    : undefined;

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
