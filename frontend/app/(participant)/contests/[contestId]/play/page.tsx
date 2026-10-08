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

import { ContentLoadedProvider, ContentLoadedSignal } from "./content-loaded";
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
 * Whether a refusal means this participant may not use this screen at all
 * right now, regardless of which of the three requests below surfaces it
 * first (finding 2). Every such refusal comes out of the same admission gate
 * `/play/story`, `/play/questions` and `/play/log` all share (`ParticipantHandler.admit`
 * on the Go side: `AdmitRead`, then `Access`) — a contest this participant's
 * own deadline has passed for, an address that stopped being allowed, an
 * account removed from the roster, or simply asking faster than this
 * installation allows. None of those is about the story, the questions or the
 * log in particular: whichever request answers with one, the workspace
 * around them would be refused for the exact same reason, so there is
 * nothing left on this screen worth keeping — it is shown the "not
 * available" page below, the same way `contest_finished` and
 * `contest_not_running` (the two this screen originally handled) already
 * were.
 *
 * `story_not_found` deliberately does not take the screen: it is a fact about the
 * story alone (`Reader.Story` refuses this way the instant a contest's story
 * has no translation for the negotiated language), and the questions, the
 * log and the console are unaffected by it — see where it is handled below
 * for why it gets its own, narrower treatment instead.
 */
function takesTheScreen(code: string): boolean {
  const kind = refusalKind(code);
  // query_too_often is named rather than taken as a kind: of the refusals
  // that pass by themselves, the gate's own rate limit is the only one
  // these reads meet.
  return (
    kind === "closed" ||
    kind === "excluded" ||
    kind === "elsewhere" ||
    code === "query_too_often" ||
    code === "question_not_found"
  );
}

/**
 * Where a participant works: the full-screen olympiad workspace (Task 3) —
 * the SQL console, the last result and the query log below it, and the story
 * and questions beside it.
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
 * (takesTheScreen's own doc, finding 2). The one exception is the
 * story missing a translation: that is a fact about the story alone, and
 * losing it must not lose the questions, the log or the console beside it.
 */
export default async function PlayPage({ params }: PageProps<"/contests/[contestId]/play">) {
  const { contestId } = await params;
  const [locale, whole] = await Promise.all([activeLocale(), activeDictionary()]);
  // Narrowed once, here, and never widened downstream: everything this screen
  // hands a Client Component is serialised into the route's payload, and the
  // sections nobody on it can read were more than half of what crossed
  // (finding 5, and `./dictionary.ts`'s own doc).
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
    // draft, finished or archived: none of these ever becomes `running`
    // while this screen stays open, so there is nothing to wait for and
    // nothing worth opening a connection over.
    return <UnavailablePage title={contest.title} body={dict.participant.play.unavailable.body} dict={dict} />;
  }

  // The shell, and the boundary the slow half of this screen sits behind
  // (finding 2).
  //
  // Everything below this point needs four API requests; the bar above the
  // workspace needs none of them. Rendering `PlayHeader` here, outside the
  // `<Suspense>`, is what puts the contest's name and a running countdown in
  // the first wave of the response — measured at 100–200ms of API time on an
  // idle installation, and seconds at the one minute this screen actually
  // matters, when three hundred participants enter the same tour at once and
  // the story, the question list, the query log and the schema are asked for
  // three hundred times over. Before this the whole page waited on all four:
  // Next holds the previous screen until a server render finishes, so a
  // participant pressing "Enter" watched the register they had just left,
  // and a hard reload showed a white page for the same interval.
  //
  // The height arithmetic lives here rather than in `Workspace` now, because
  // this is the element that holds the bar and the panes together: `h-12`
  // for the product's own app bar plus the pixel of its bottom border, and
  // only from `narrow` up — below the breakpoint this screen is an ordinary
  // scrolling stack of sections (see `Workspace`'s own doc).
  //
  // `print:contents` because that arithmetic is this box's whole job, and a
  // sheet of paper has no viewport height to hold to: without it the print
  // copy of the story `Workspace` carries would be clipped at one screen.
  return (
    <div className="flex min-h-0 flex-col print:contents narrow:h-[calc(100dvh-3rem-1px)]">
      {/* Two providers, both of them carrying something across the Suspense
          boundary between the header and the workspace, in opposite
          directions. `ContentLoadedProvider` carries one fact upward: the
          workspace's content reads succeeded, which under individual timing
          means the participant's clock has started (content-loaded.tsx).
          `PanelVisibilityProvider` holds what the header's own toggles do to
          the panels below them (§8, panel-toggles.tsx) — state that belongs
          to neither sibling on its own. */}
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
 * The four requests the workspace is built from, and the workspace itself.
 *
 * A component of its own purely so there is something for `<Suspense>` to
 * suspend on: a boundary only defers what is *inside* it, and until this was
 * split out the whole page — the header included — was one async function
 * and therefore one wait.
 *
 * A refusal that takes this whole screen away (takesTheScreen) is
 * answered here rather than by the page, and so is shown beneath the bar
 * instead of replacing the page with a heading of its own. That is the one
 * visible consequence of the split, and it is the better screen: the
 * contest's name is above it already, and repeating it under itself was
 * never the point of that heading.
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
  /** Carried for the print copy's byline and for the title on the story's own cover — the bar above shows it too. */
  contestTitle: string;
  /** The picture this contest wears, from the same listing the title came from. Empty for a contest that wears a drawn cover. */
  coverHash: string;
  /** Who made that picture (design spec §10.1). Empty for a drawn cover, which has nobody to credit. */
  coverAttribution: string;
  /** The contest's own scoring mode, from the summary this route already read — see `questions-panel.tsx` for what ICPC changes on this screen. */
  scoring: ContestSummary["scoring"];
  /** Minutes added for a wrong attempt on a question later solved, read only while `scoring` is `icpc`. */
  icpcPenaltyMin: number;
  locale: Locale;
  dict: PlayDictionary;
}) {
  const errors = dict.errors as Record<string, string>;

  // Fetched together, and answered mostly independently (finding 2): the
  // three requests share the same admission gate, so a refusal that is
  // really about this participant's own access to the contest (see
  // takesTheScreen) means the same thing regardless of which
  // settles first. But a refusal that is only about the story itself — it
  // has no translation for this language — has nothing to do with whether
  // the questions list, the log or the console still work, so `Promise.all`
  // (which would fail the whole page on either rejecting) is deliberately
  // not used here; `allSettled` lets each answer be read on its own.
  //
  // Under individual timing these reads are also what starts the
  // participant's clock: the API starts it on the first successful read of the
  // story, the questions or the schema, so the countdown covers the time spent
  // reading the contest and not only the time spent typing. Any of the three
  // may be the one that starts it; the server starts it once. The
  // workspace read (notes and SQL tabs) never starts it: keeping notes is
  // not reading the contest.
  //
  // `lang` on the workspace read names the first SQL tab the server creates
  // for a participant who has none yet.
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

  // The story: read on success, or reduced to a shown reason on the one
  // refusal that is about the story alone. Anything else — including a
  // takesTheScreen refusal reaching this request instead of one of
  // the others — is answered the same way the questions list's own refusal
  // above is, since it means the same thing no matter which request it
  // arrived on.
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

  // The query log's own first page: read on success, or — unless the
  // refusal is one that takesTheScreen, in which case the whole
  // screen is unavailable exactly as it would be for the other two — quietly
  // reduced to an empty page rather than failing this whole screen. The log
  // is a record of what already happened, not something the console needs to
  // function: a participant should still be able to read the story and run
  // queries even if this one read failed (a transient database error, say),
  // and QueryLogPanel's own client-side retry gets another chance at it.
  //
  // `failed` carries which of those two happened (finding 4): an empty log
  // and a log this request could not read degrade to the exact same
  // `{items: [], total: 0}` shape otherwise, and QueryLogPanel rendered them
  // as the identical "you have not run a query yet" — indistinguishable from
  // a real answer, and with no way to retry since a `total` of zero hides
  // "load more" too.
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
    // Any other failure (a transient 500, an unreachable API): degrade
    // rather than crash, but say so — see `failed`'s own doc just above.
    initialLog = { items: [], total: 0, failed: true };
  }

  // The game's shape, for the console's schema panel. Absent rather than
  // empty on every refusal, including the one that is a rule of the game
  // rather than a fault: a contest that closed its catalogues answers
  // `schema_hidden`, and discovering the shape is the puzzle there. No
  // refusal of this read may take the screen down — a participant can play
  // an olympiad without the panel, and could before it existed.
  let schema: GameSchema | null = null;
  if (schemaResult.status === "fulfilled") {
    schema = gameSchemaSchema.parse(schemaResult.value);
  }

  // The participant's notes and SQL tabs. No refusal of this read, and no
  // answer this build cannot parse, takes the screen down: the notes say they
  // could not be loaded, and the rest works as before. A refusal that is
  // about the whole screen has already been answered above by the requests
  // that share its admission gate.
  let workspace: WorkspaceSnapshot | null = null;
  if (workspaceResult.status === "fulfilled") {
    const parsed = workspaceSchema.safeParse(workspaceResult.value);
    workspace = parsed.success ? parsed.data : null;
  }

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

  // The print-only copy of the story (workspace.tsx renders it, hidden until
  // `@media print`) carries a byline the same way the old `.../play/print`
  // route did — who is printing, and when. `fetchIdentity` is wrapped in
  // React's own `cache()`, so asking again here costs nothing beyond
  // `ParticipantLayout`'s own call for the account chip: same request, same
  // memoised promise. Tolerated the same way that route tolerated it: this
  // only decorates a byline, it never gates on one, and `PrintView` already
  // reads an empty name as "say only the date" rather than a broken sentence.
  const identity = await fetchIdentity().catch(() => null);
  const participantName = identity ? identity.fullName || identity.login : "";
  const printedOn = formatDay(new Date().toISOString(), { locale });

  // Rendered here, on the server, and handed to `Workspace` as finished
  // markup rather than as Markdown for the browser to parse — the same
  // decision the questions above and the story below already make, applied to
  // the one copy that was still getting it wrong.
  //
  // `Workspace` is a client component, so importing `PrintView` from it put
  // `react-markdown` and `remark-gfm` in the client graph of the one route
  // whose time-to-interactive matters most: measured, 31.9 KiB gzipped of
  // parser, on the screen a participant sits in front of for two hours. And
  // because the print copy is mounted the whole time (hidden until
  // `@media print`, see `Workspace`'s own doc), the browser parsed the story
  // a second time on every mount to build a subtree nobody would look at
  // unless they printed. Both are gone by moving the render to this side of
  // the wire; what crosses it now is the same elements the on-screen story
  // already crosses as.
  //
  // Null exactly when there is no story, which is what the print container in
  // `Workspace` mirrors — and what `side-panel.tsx` already mirrors in not
  // offering the print control at all in that state.
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

  // The picture above the story (design spec §10), rendered on this side of
  // the wire for the same reason the story and the print copy beside it
  // already are: `Workspace` is a Client Component, and what it is handed is
  // serialised into this route's payload either way — but rendering it here
  // keeps `DrawnCover`'s geometry and `coverHref` out of the client graph of
  // the screen whose time-to-interactive matters most in the product.
  //
  // Null exactly when there is no story, which is what `SidePanel` mirrors
  // in showing the reason instead: a cover heads a story, and a tab saying
  // why there is none is not a story to head.
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

  // The signal mounts only here, on the path where the content reads
  // succeeded, and tells the header above the boundary so (content-loaded.tsx).
  return (
    <>
      <ContentLoadedSignal />
      <Workspace
        // Read above for the print byline, and React's `cache()` makes the
        // second ask free. Null when that read failed, and then the screen
        // keeps no drafts at all rather than keeping them where another
        // account could read them.
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
 * A refusal that takes the workspace away, shown under the bar that is
 * already on screen.
 *
 * The same three dictionary sentences `UnavailablePage` below shows, minus
 * the heading: this renders inside the shell `PlayPage` has already streamed,
 * where the contest's title is the first thing above it (finding 2). The
 * rate-limit case keeps its way back for exactly the reason recorded on
 * `UnavailablePage` — that one lifts by itself within the minute, and the
 * others do not lift at all.
 */
function ScreenUnavailable({ body, code, dict }: { body: string; code?: string; dict: PlayDictionary }) {
  return (
    <div className="flex min-h-0 flex-col items-start gap-4 p-10 max-narrow:p-4.5 narrow:flex-1">
      <p className="max-w-body text-body text-ink">{body}</p>
      {code === "query_too_often" ? <ReloadLink dict={dict} /> : null}
    </div>
  );
}

/**
 * The plain "nothing to show" screen.
 *
 * One of the codes that lands here is not like the others, and treating it
 * the same took a participant out of an olympiad for reloading. A rate limit
 * lifts by itself within the minute; `contest_finished`, `not_a_participant`
 * and an address outside the network do not lift at all. SPEC.md's own state
 * list calls the first `blocked` and requires it to carry "the reason and the
 * moment it lifts", so it says so and offers the way back — which is the same
 * address, once the minute has passed.
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
 * The room a participant who arrived early sits in.
 *
 * It holds the one events connection this screen has for a contest that has
 * not started, and asks the server for the real page — the workspace itself
 * — the instant `contest_started` arrives (`PlayHeader`'s own doc):
 * nothing here polls, and nothing here guesses.
 *
 * This is an ordinary content page rather than the workspace — nobody is
 * typing SQL yet — so it keeps `Band`'s hatched fields rather than the
 * full-bleed exception `Workspace`'s own doc records.
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
      {/* PlayHeader's own border is meant to run the full width of the
          content column, not stop at it (its own doc: "edge to edge").
          Inside `Band`, that column carries `px-10`/`max-narrow:px-4.5` of
          padding this bar sits under like any other content — so without
          this negative margin cancelling exactly that padding, the border
          stopped short of both edges (finding 7, the regression the
          implementer flagged). The workspace's own use of PlayHeader needs
          none of this: it renders entirely outside `Band`, already full
          width. */}
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
