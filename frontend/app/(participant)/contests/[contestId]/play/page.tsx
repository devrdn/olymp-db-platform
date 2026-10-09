import { Suspense } from "react";

import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { StoryText } from "@/components/product/story-text";
import { ApiError } from "@/lib/api/client";
import { contestListSchema, type ContestSummary } from "@/lib/api/contests";
import { playQuestionListSchema, playStorySchema } from "@/lib/api/play";
import { queryLogResponseSchema, type QueryLogEntry } from "@/lib/api/querylog";
import { QUERY_LOG_PAGE_SIZE } from "@/lib/api/querylog-terms";
import { gameSchemaSchema, type GameSchema } from "@/lib/api/schema";
import { serverRequest } from "@/lib/api/server";
import { workspaceSchema, type WorkspaceSnapshot } from "@/lib/api/workspace";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { fetchIdentity } from "@/lib/auth/session";
import { formatDay, formatMoment } from "@/lib/format/datetime";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import type { Locale } from "@/lib/i18n/config";

import { ContentLoadedProvider, ContentLoadedSignal, RenderedRefusalSignal } from "./content-loaded";
import { playDictionary, type PlayDictionary } from "./dictionary";
import { PanelVisibilityProvider } from "./panel-toggles";
import { PlayHeader } from "./play-header";
import { PrintView } from "./print-view";
import { refusalKind } from "./refusals";
import { StoryCover } from "./story-cover";
import type { QuestionEntry } from "./questions-panel";
import { ReloadLink } from "./reload-link";
import { WorkspaceSkeleton } from "./skeleton";
import { Workspace } from "./workspace";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.participant.console.heading };
}

/**
 * Whether a refusal means this participant may not use the screen at all
 * right now, whichever request surfaces it. Such refusals come from the
 * admission gate the `/play/*` reads share (`AdmitRead`, then `Access`): a
 * passed deadline, a disallowed address, removal from the roster, or the
 * rate limit. `question_not_found` is listed too, though none of the reads
 * below answers with it. `story_not_found` does not take the screen: it
 * concerns the story alone and is handled below.
 */
function takesTheScreen(code: string): boolean {
  const kind = refusalKind(code);
  // query_too_often is named rather than matched by kind: it is the only
  // self-lifting refusal these reads meet.
  return (
    kind === "closed" ||
    kind === "dormant" ||
    kind === "excluded" ||
    kind === "elsewhere" ||
    code === "query_too_often" ||
    code === "question_not_found"
  );
}

/**
 * The participant's olympiad screen: the SQL console, the last result and the
 * query log below it, the story and questions beside it.
 *
 * The contest is read from the participant's enrolled listing, since the
 * contest endpoint needs a staff permission. The `/play/*` endpoints admit
 * only `running`, so other statuses are answered here: `published` opens a
 * waiting room on the events channel, and anything else is "not available"
 * with no connection, since it will never become `running` while the screen
 * is open.
 *
 * A running contest can still refuse this participant (see takesTheScreen);
 * that is shown as unavailable, not as a failed load.
 */
export default async function PlayPage({ params }: PageProps<"/contests/[contestId]/play">) {
  const { contestId } = await params;
  const [locale, whole] = await Promise.all([activeLocale(), activeDictionary()]);
  // Narrowed once and never widened: everything handed to a Client Component
  // is serialised into the payload (`./dictionary.ts`).
  const dict = playDictionary(whole);

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
    // draft, finished or archived: none ever becomes `running` while this
    // screen is open, so there is nothing to wait for.
    return <UnavailablePage title={contest.title} body={dict.participant.play.unavailable.body} dict={dict} />;
  }

  // `PlayHeader` renders outside the Suspense boundary so the title and the
  // countdown stream at once: the API reads behind it take seconds when three
  // hundred participants enter together, and Next would otherwise hold the
  // previous screen until they finish.
  //
  // The height subtracts the app bar's `h-12` plus its 1px bottom border, and
  // applies only from `narrow` up. `print:contents` drops it for print, where
  // it would clip the print copy of the story at one screen.
  return (
    <div className="flex min-h-0 flex-col print:contents narrow:h-[calc(100dvh-3rem-1px)]">
      {/* Both providers carry state across the Suspense boundary.
          `ContentLoadedProvider` tells the header the content reads succeeded,
          which under individual timing means the clock started
          (content-loaded.tsx). `PanelVisibilityProvider` holds the panel toggles
          shared by the header and the workspace. */}
      <ContentLoadedProvider>
        <PanelVisibilityProvider contestId={contestId}>
          <PlayHeader contestId={contestId} title={contest.title} waitingForStart={false} dict={dict} />
          <Suspense fallback={<WorkspaceSkeleton dict={dict} />}>
            <PlayPanels
              contestId={contestId}
              contestTitle={contest.title}
              coverHash={contest.coverHash}
              coverAttribution={contest.coverAttribution}
              scoring={contest.scoring}
              icpcPenaltyMin={contest.icpcPenaltyMin}
              locale={locale}
              dict={dict}
            />
          </Suspense>
        </PanelVisibilityProvider>
      </ContentLoadedProvider>
    </div>
  );
}

/**
 * The requests the workspace is built from, and the workspace itself; a
 * separate component so `<Suspense>` has something to suspend on. A refusal
 * that takes the screen is therefore shown beneath the header.
 */
async function PlayPanels({
  contestId,
  contestTitle,
  coverHash,
  coverAttribution,
  scoring,
  icpcPenaltyMin,
  locale,
  dict,
}: {
  contestId: string;
  /** For the print copy's byline and the story cover's title. */
  contestTitle: string;
  /** The contest's cover picture; empty for a drawn cover. */
  coverHash: string;
  /** Who made that picture (SPEC.md §10.1); empty for a drawn cover. */
  coverAttribution: string;
  /** The contest's scoring mode; see `questions-panel.tsx` for what ICPC changes. */
  scoring: ContestSummary["scoring"];
  /** Minutes added per wrong attempt on a question later solved; read only under `icpc`. */
  icpcPenaltyMin: number;
  locale: Locale;
  dict: PlayDictionary;
}) {
  const errors = dict.errors as Record<string, string>;

  // `allSettled`, not `all`: a refusal about the story alone must not lose the
  // questions, the log or the console. A refusal from the shared admission
  // gate means the same whichever request carries it (takesTheScreen).
  //
  // Under individual timing the first successful read of the story, the
  // questions or the schema starts the participant's clock (the server starts
  // it once); the workspace read never does. `lang` on the workspace read
  // names the first SQL tab the server creates.
  const [storyResult, questionsResult, logResult, schemaResult, workspaceResult] = await Promise.allSettled([
    serverRequest(`/contests/${contestId}/play/story?lang=${locale}`),
    serverRequest(`/contests/${contestId}/play/questions?lang=${locale}`),
    serverRequest(`/contests/${contestId}/play/log?limit=${QUERY_LOG_PAGE_SIZE}&offset=0`),
    serverRequest(`/contests/${contestId}/play/schema`),
    serverRequest(`/contests/${contestId}/play/workspace?lang=${locale}`),
  ]);

  if (questionsResult.status === "rejected") {
    const error = questionsResult.reason;
    if (error instanceof ApiError && takesTheScreen(error.code)) {
      return <ScreenUnavailable body={errors[error.code]} code={error.code} dict={dict} />;
    }
    throw error;
  }
  const questions = playQuestionListSchema.parse(questionsResult.value).items;

  // The story, or a shown reason on `story_not_found`. A screen-taking refusal
  // here is answered as for the questions.
  let storyBody: string | null = null;
  let storyUnavailable: string | null = null;
  if (storyResult.status === "fulfilled") {
    storyBody = playStorySchema.parse(storyResult.value).bodyMd;
  } else {
    const error = storyResult.reason;
    if (error instanceof ApiError && takesTheScreen(error.code)) {
      return <ScreenUnavailable body={errors[error.code]} code={error.code} dict={dict} />;
    }
    if (error instanceof ApiError && error.code === "story_not_found") {
      storyUnavailable = errors.story_not_found;
    } else {
      throw error;
    }
  }

  // The log degrades to an empty page on any failure that does not take the
  // screen: it is a record, not something the console needs, and
  // QueryLogPanel retries on the client. `failed` tells an unread log from an
  // empty one, which would otherwise render as "no queries yet" with no retry.
  let initialLog: { items: QueryLogEntry[]; total: number; failed: boolean } = {
    items: [],
    total: 0,
    failed: false,
  };
  if (logResult.status === "fulfilled") {
    initialLog = { ...queryLogResponseSchema.parse(logResult.value), failed: false };
  } else {
    const error = logResult.reason;
    if (error instanceof ApiError && takesTheScreen(error.code)) {
      return <ScreenUnavailable body={errors[error.code]} code={error.code} dict={dict} />;
    }
    // Degrade rather than crash, but say so.
    initialLog = { items: [], total: 0, failed: true };
  }

  // Absent on every refusal, including `schema_hidden`, where discovering the
  // shape is the puzzle. No refusal of this read takes the screen down.
  let schema: GameSchema | null = null;
  if (schemaResult.status === "fulfilled") {
    schema = gameSchemaSchema.parse(schemaResult.value);
  }

  // Neither a refusal nor an unparseable answer takes the screen down: the
  // notes say they could not be loaded.
  let workspace: WorkspaceSnapshot | null = null;
  if (workspaceResult.status === "fulfilled") {
    const parsed = workspaceSchema.safeParse(workspaceResult.value);
    workspace = parsed.success ? parsed.data : null;
  }

  // The questions, the story and its print copy are rendered here, on the
  // server, so the browser never ships the Markdown parser (about 32 KiB
  // gzipped) or parses the same text twice.
  const questionEntries: QuestionEntry[] = questions.map((question, index) => ({
    question,
    index: index + 1,
    body: <StoryText key={question.id} markdown={question.bodyMd} className="max-w-none font-sans text-body" />,
  }));

  // The print copy's byline: who is printing, and when. `fetchIdentity` is
  // wrapped in React's `cache()`, so this shares `ParticipantLayout`'s
  // request. A failed read only drops the name; `PrintView` then prints the
  // date alone.
  const identity = await fetchIdentity().catch(() => null);
  const participantName = identity ? identity.fullName || identity.login : "";
  const printedOn = formatDay(new Date().toISOString(), { locale });

  // Null exactly when there is no story, as the print container in
  // `Workspace` and the print control in `side-panel.tsx` expect.
  const printView =
    storyBody !== null ? (
      <PrintView
        contestTitle={contestTitle}
        participantName={participantName}
        date={printedOn}
        storyMarkdown={storyBody}
        dict={dict}
      />
    ) : null;

  // The picture above the story (SPEC.md §10), rendered on the server to
  // keep `DrawnCover`'s geometry and `coverHref` out of the client graph. Null
  // when there is no story: `SidePanel` shows the reason instead.
  const storyCover =
    storyBody !== null ? (
      <StoryCover
        contestId={contestId}
        title={contestTitle}
        coverHash={coverHash}
        coverAttribution={coverAttribution}
        dict={dict}
      />
    ) : null;

  // Mounted only when the content reads succeeded; it tells the header so
  // (content-loaded.tsx).
  return (
    <>
      <ContentLoadedSignal />
      <Workspace
        // Null when the identity read failed; the screen then keeps no drafts
        // rather than keep them where another account could read them.
        accountId={identity?.id ?? null}
        contestId={contestId}
        storyBody={storyBody !== null ? <StoryText markdown={storyBody} /> : null}
        storyCover={storyCover}
        printView={printView}
        storyUnavailable={storyUnavailable}
        questionEntries={questionEntries}
        scoring={scoring}
        icpcPenaltyMin={icpcPenaltyMin}
        schema={schema}
        initialLog={initialLog}
        workspace={workspace}
        locale={locale}
        dict={dict}
      />
    </>
  );
}

/**
 * A refusal that takes the workspace away, shown under the header already on
 * screen. It tells the header which refusal it shows (content-loaded.tsx): a
 * contest that was not open can open before the header's channel connects,
 * and the header refreshes the page once it knows both.
 */
function ScreenUnavailable({ body, code, dict }: { body: string; code?: string; dict: PlayDictionary }) {
  return (
    <div className="flex min-h-0 flex-col items-start gap-4 p-10 max-narrow:p-4.5 narrow:flex-1">
      {code !== undefined ? <RenderedRefusalSignal code={code} /> : null}
      <p className="max-w-body text-body text-ink">{body}</p>
      {code === "query_too_often" ? <ReloadLink dict={dict} /> : null}
    </div>
  );
}

/**
 * The plain "nothing to show" screen. A rate limit lifts within the minute,
 * unlike every other code here, so it offers a reload (SPEC.md's `blocked`
 * state carries the reason and when it lifts).
 */
function UnavailablePage({
  title,
  body,
  code,
  dict,
}: {
  title: string;
  body: string;
  code?: string;
  dict: PlayDictionary;
}) {
  return (
    <Band fill>
      <div className="flex flex-col gap-4">
        <h1 className="text-h2 text-ink">{title}</h1>
        <p className="max-w-body text-body text-ink">{body}</p>
        {code === "query_too_often" ? <ReloadLink dict={dict} /> : null}
      </div>
    </Band>
  );
}

/**
 * Where a participant who arrived early waits. It holds the events
 * connection and reloads into the workspace the instant `contest_started`
 * arrives (`PlayHeader`); nothing here polls. An ordinary content page, so it
 * keeps `Band`'s hatched fields.
 */
function WaitingRoom({
  contest,
  locale,
  dict,
}: {
  contest: ContestSummary;
  locale: Locale;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.waiting;
  // formatMoment uses the installation's timezone: without an explicit zone
  // this Server Component would format in the server's zone (UTC in a
  // container) and disagree with the browser that hydrates it.
  const startsAt = contest.startsAt ? formatMoment(contest.startsAt, { locale }) : undefined;

  return (
    <div className="flex flex-col gap-6">
      {/* Cancels `Band`'s padding so the header's border runs edge to edge. */}
      <div className="-mx-10 max-narrow:-mx-4.5">
        <PlayHeader contestId={contest.id} title={contest.title} waitingForStart dict={dict} />
      </div>
      <div className="flex max-w-body flex-col gap-2">
        <p className="text-body text-ink">{t.body}</p>
        {startsAt ? <p className="text-small text-ink-2">{t.startsAt.replace("{time}", startsAt)}</p> : null}
      </div>
    </div>
  );
}
