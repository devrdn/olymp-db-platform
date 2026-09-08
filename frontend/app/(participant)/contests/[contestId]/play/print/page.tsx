import { redirect } from "next/navigation";

import { ApiError } from "@/lib/api/client";
import { contestListSchema } from "@/lib/api/contests";
import { playStorySchema } from "@/lib/api/play";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { fetchIdentity } from "@/lib/auth/session";
import { formatDay } from "@/lib/format/datetime";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { PrintView } from "./print-view";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.participant.play.print.button };
}

/**
 * `.../play/print`: the story as a page meant to be printed, not the play
 * screen under a print stylesheet.
 *
 * The play screen is fixed and full-bleed on purpose — `100dvh` panels that
 * scroll internally, never the document itself (`workspace.tsx`'s own doc,
 * and `docs/design/SPEC.md` §5's own exception for it). Printing that
 * directly needs a stylesheet that undoes every one of those heights and
 * every `overflow` on the way to the story, on a tree that also holds the
 * console, the schema panel, the result table and the query log — each of
 * which then needs its own `display: none` for print, correctly, forever, as
 * the workspace changes under it. A route that never renders any of that in
 * the first place cannot leak a table cell of the wrong panel into a printed
 * page no matter what a future edit to the workspace does; there is nothing
 * here for such an edit to reach.
 *
 * It is also what `renderToString` — the tool that can actually check
 * pagination, since jsdom lays nothing out — needs: `PrintView` takes plain
 * props and fetches nothing, so it can be rendered on its own with the
 * built stylesheet and no session, no cookies, and no SSE channel to fake.
 * Rendering a "print mode" of the whole workspace would mean faking all of
 * that just to check a page break.
 *
 * Admission is the same `/play/story` the story tab reads — this route asks
 * nothing of its own. A refusal (contest not running, no story in this
 * language, the caller no longer on the roster) is shown as the dictionary's
 * own sentence for it rather than the full `PlayPage` unavailable screen:
 * there is no waiting room here, and nothing on this route ever becomes
 * available by being left open the way the workspace's own SSE-driven one
 * does.
 */
export default async function PrintPage({ params }: PageProps<"/contests/[contestId]/play/print">) {
  const { contestId } = await params;
  const [locale, dict] = await Promise.all([activeLocale(), activeDictionary()]);
  const errors = dict.errors as Record<string, string>;

  const search = new URLSearchParams({ scope: "participant", enrolled: "true", lang: locale });
  const listing = await serverRequest(`/contests?${search}`).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, `/contests/${contestId}/play/print`);
    if (target) redirect(target);
    throw error;
  });
  const contest = contestListSchema.parse(listing).items.find((item) => item.id === contestId);
  if (!contest) redirect("/my");

  let storyBody: string;
  try {
    const payload = await serverRequest(`/contests/${contestId}/play/story?lang=${locale}`);
    storyBody = playStorySchema.parse(payload).bodyMd;
  } catch (error) {
    if (!(error instanceof ApiError)) throw error;
    return <PrintUnavailable title={contest.title} body={errors[error.code] ?? errors.fallback} />;
  }

  // Tolerated, the same way the layout above this route tolerates it
  // (`ParticipantLayout`'s own doc): this screen only decorates the byline
  // with a name, it never gates on one, and `PrintView` already reads an
  // empty name as "say only the date" rather than a broken sentence.
  const identity = await fetchIdentity().catch(() => null);
  const participantName = identity ? identity.fullName || identity.login : "";

  const date = formatDay(new Date().toISOString(), { locale });

  return (
    <PrintView
      contestTitle={contest.title}
      participantName={participantName}
      date={date}
      storyMarkdown={storyBody}
      dict={dict}
    />
  );
}

/** The plain "nothing to print" state — this route's own, smaller than PlayPage's `UnavailablePage`: there is no rate-limit retry link and no waiting room to offer here. */
function PrintUnavailable({ title, body }: { title: string; body: string }) {
  return (
    <div className="mx-auto flex max-w-narrative flex-col gap-4 px-6 py-10">
      <h1 className="text-h2 text-ink">{title}</h1>
      <p className="text-body text-ink">{body}</p>
    </div>
  );
}
