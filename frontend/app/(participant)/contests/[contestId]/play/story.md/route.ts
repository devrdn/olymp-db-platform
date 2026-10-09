import { ApiError } from "@/lib/api/client";
import { contestListSchema } from "@/lib/api/contests";
import { playStorySchema } from "@/lib/api/play";
import { serverRequest } from "@/lib/api/server";
import { cleanEditorMarkdown } from "@/lib/format/markdown";
import { activeLocale } from "@/lib/i18n/server";

/**
 * GET .../play/story.md: the contest's title and this participant's story as
 * a Markdown file, the counterpart of the query log's CSV
 * (docs/ARCHITECTURE.md §9.1).
 *
 * Admission is not reimplemented: `serverRequest` forwards the session to
 * `GET /contests/{id}/play/story`, the endpoint the story tab reads, and any
 * `ApiError` it raises is relayed as this route's response.
 *
 * A Route Handler rather than a link to the Go API so the Markdown passes
 * through `cleanEditorMarkdown` (lib/format/markdown.ts) on the way out; that
 * rule has one implementation, in TypeScript, and a Go copy could drift.
 *
 * The title comes from a second read of the enrolled listing, since
 * `/play/story` carries none. If that read fails the download fails, rather
 * than heading the file with a bare identifier.
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
        // The identifier, not the title: the header is ASCII and a title is
        // authored text in any script (as in `queryLogCSV`).
        "Content-Disposition": `attachment; filename="story-${contestId}.md"`,
      },
    });
  } catch (error) {
    return failure(error);
  }
}

/**
 * The contest's title in the negotiated language, from the enrolled listing
 * `page.tsx` reads. A contest missing from it means the session no longer
 * takes part, so this throws the `not_a_participant` refusal `/play/story`
 * would raise.
 */
async function contestTitle(contestId: string, locale: string): Promise<string> {
  const search = new URLSearchParams({ scope: "participant", enrolled: "true", lang: locale });
  const payload = await serverRequest(`/contests?${search}`);
  const contest = contestListSchema.parse(payload).items.find((item) => item.id === contestId);
  if (!contest) throw new ApiError("not_a_participant", 403, "The caller is not taking part in this contest");
  return contest.title;
}

/**
 * Turns a failure into this route's response. An `ApiError` is relayed with
 * the API's status and English message: a download link cannot read a JSON
 * envelope to pick a translated sentence, so the browser shows the status
 * line. Anything else is a 500 with no detail.
 */
function failure(error: unknown): Response {
  if (error instanceof ApiError) {
    return new Response(error.message, { status: error.status });
  }
  return new Response("Internal server error", { status: 500 });
}
