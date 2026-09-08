import { ApiError } from "@/lib/api/client";
import { contestListSchema } from "@/lib/api/contests";
import { playStorySchema } from "@/lib/api/play";
import { serverRequest } from "@/lib/api/server";
import { cleanEditorMarkdown } from "@/lib/format/markdown";
import { activeLocale } from "@/lib/i18n/server";

/**
 * GET .../play/story.md: the contest's title and this participant's own
 * story, as a file — the Markdown counterpart to
 * `backend/internal/api/participant_handler.go`'s `queryLogCSV` (§9.1), and
 * the last export the plan asks for.
 *
 * Admission is not reimplemented here. `serverRequest` carries the session on
 * to `GET /contests/{id}/play/story`, the very endpoint the story tab already
 * reads, so this route asks exactly the same question
 * (`queryproxy.Service.Access`, negotiated to the same language) rather than
 * a second opinion of its own — an ApiError it raises (contest finished, not
 * a participant, an address outside the network, no story in this language)
 * is simply relayed as this route's own response below.
 *
 * The Markdown is cleaned here, once, on the way out — this is a Next.js
 * Route Handler rather than a plain link to the Go API precisely so that it
 * can: `cleanEditorMarkdown` (lib/format/markdown.ts) exists because the
 * WYSIWYG editor used to store a literal `<br />` for an empty paragraph, and
 * that function's own doc already names an export as the reason it runs "on
 * the way out" and not only on the way in. Its one implementation is
 * TypeScript, proven by the tests beside it; porting the same regexes into Go
 * for this one route would leave two places that could each be right about a
 * story and disagree about the next one — the very drift `cleanEditorMarkdown`'s
 * doc calls out as the reason a migration was rejected in favour of cleaning
 * at every "way out" instead. A Route Handler is still a server deciding what
 * leaves it (SPEC.md §11.1's own test), it is just this application's server
 * rather than the Go one — `ExportMenu` does not care which origin its href
 * names, only that nothing is fetched and assembled into a blob in the page,
 * and nothing here is.
 *
 * Two calls rather than one: `/play/story` has never carried a title (it
 * answers "the story in this language", nothing about the contest around
 * it), and adding one would be a wire-shape change for a field the story tab
 * itself has never needed, since `page.tsx` already has the title from the
 * participant's own enrolled listing. This route reads that same listing.
 * Both calls share one session and one admission; if the title read fails
 * while the story read just succeeded, that is not "close enough" to hand
 * over a file headed by a bare identifier — it is answered as a failure
 * below, the same as if the story itself had failed.
 */
export async function GET(
  _request: Request,
  { params }: { params: Promise<{ contestId: string }> },
): Promise<Response> {
  const { contestId } = await params;
  const locale = await activeLocale();

  try {
    const [story, contest] = await Promise.all([
      serverRequest(`/contests/${contestId}/play/story?lang=${locale}`).then((payload) =>
        playStorySchema.parse(payload),
      ),
      contestTitle(contestId, locale),
    ]);

    const markdown = `# ${contest}\n\n${cleanEditorMarkdown(story.bodyMd)}\n`;

    return new Response(markdown, {
      status: 200,
      headers: {
        "Content-Type": "text/markdown; charset=utf-8",
        // The identifier, not the title: a title is authored text in any
        // script and this header is ASCII — participant_handler.go's own
        // `queryLogCSV` and `contest_package.go` name their files the same
        // way, for the same reason.
        "Content-Disposition": `attachment; filename="story-${contestId}.md"`,
      },
    });
  } catch (error) {
    return failure(error);
  }
}

/**
 * The contest's own title, in the negotiated language, from the same
 * enrolled listing `page.tsx` reads to build the workspace's header. Throws
 * the same `ApiError` `/play/story` itself would raise for a caller who is
 * not enrolled (`failure` below turns it into a 403) when the listing does
 * not carry this contest at all — which can only mean the session behind
 * this request is no longer taking part in it, the exact fact `/play/story`
 * would already have refused on.
 */
async function contestTitle(contestId: string, locale: string): Promise<string> {
  const search = new URLSearchParams({ scope: "participant", enrolled: "true", lang: locale });
  const payload = await serverRequest(`/contests?${search}`);
  const contest = contestListSchema.parse(payload).items.find((item) => item.id === contestId);
  if (!contest) throw new ApiError("not_a_participant", 403, "The caller is not taking part in this contest");
  return contest.title;
}

/**
 * Turns whatever `serverRequest` or the schemas above raised into this
 * route's own response.
 *
 * `ApiError` is relayed with the API's own status and message: a download
 * link, unlike a fetch the page already handles, gets no chance to read a
 * JSON envelope and pick a translated sentence out of it, so the plainest
 * honest thing this route can do is the status and the sentence the API
 * already wrote in English (the same text `ApiError.message` carries
 * everywhere else in this codebase, and — per its own doc — never rendered to
 * a participant; here there is no dictionary to render it through in the
 * first place, only a status line a browser shows on a failed download).
 * Anything else — a schema that did not parse, a network failure `serverRequest`
 * itself could not turn into an `ApiError` — is a 500 with no detail to leak.
 */
function failure(error: unknown): Response {
  if (error instanceof ApiError) {
    return new Response(error.message, { status: error.status });
  }
  return new Response("Internal server error", { status: 500 });
}
