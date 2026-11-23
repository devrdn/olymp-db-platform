import { readPage, Refusal } from "@/lib/api/page-read";
import { playQuestionListSchema, playStorySchema, type PlayQuestion, type PlayStory } from "@/lib/api/play";
import { queryLogResponseSchema, type QueryLogEntry } from "@/lib/api/querylog";
import { QUERY_LOG_PAGE_SIZE } from "@/lib/api/querylog-terms";
import { gameSchemaSchema, type GameSchema } from "@/lib/api/schema";
import { workspaceSchema, type WorkspaceSnapshot } from "@/lib/api/workspace";
import type { Locale } from "@/lib/i18n/config";

import { isReadRateLimit, NAMED_CODES, refusalKind } from "./refusals";

/**
 * The refusals that mean this participant may not use the screen at all right
 * now, whichever read surfaces them. They come from the admission the
 * `/play/*` reads share (the read budget, then the gate): a passed deadline, a
 * disallowed address, removal from the roster, or the read rate limit, the
 * only self-lifting refusal these reads meet. `question_not_found` is listed
 * too, though none of the reads answers with it.
 */
const TAKES_THE_SCREEN: readonly string[] = [
  ...NAMED_CODES.filter((code) => {
    const kind = refusalKind(code);
    return kind === "closed" || kind === "dormant" || kind === "excluded" || kind === "elsewhere" || isReadRateLimit(code);
  }),
  "question_not_found",
];

/** What the workspace is built from, or the refusal that takes the screen instead. */
export type PlayReads =
  | { kind: "unavailable"; code: string }
  | {
      kind: "ready";
      questions: PlayQuestion[];
      /** Null on `story_not_found`, which concerns the story alone. */
      story: PlayStory | null;
      /** `failed` tells an unread log from an empty one, which would read as "no queries yet". */
      log: { items: QueryLogEntry[]; total: number; failed: boolean };
      /** Null on every refusal, `schema_hidden` included, where discovering the shape is the puzzle. */
      schema: GameSchema | null;
      /** Null when the notes could not be read; they say so. */
      workspace: WorkspaceSnapshot | null;
    };

/**
 * The play screen's reads, together. Settled apart, so a refusal about the
 * story alone does not lose the rest, and answered in a fixed order, so the
 * questions' answer wins over the story's. The log, the schema and the
 * workspace are best-effort: the console works without them, and the log
 * panel retries on the client.
 *
 * Under individual timing the first successful read of the story, the
 * questions or the schema starts the participant's clock (the server starts
 * it once); the workspace read never does. `lang` on the workspace read names
 * the first SQL tab the server creates.
 */
export async function readPlay(contestId: string, locale: Locale): Promise<PlayReads> {
  const base = `/contests/${contestId}/play`;
  // The listing already found the contest, so the gate's refusals are the
  // screen's to show; any of them on a best-effort read is the gate's answer,
  // not a failure worth a log line.
  const policy = { here: base, shown: TAKES_THE_SCREEN };
  const quiet = { here: base, absent: TAKES_THE_SCREEN, bestEffort: true } as const;

  const [questionsRead, storyRead, logRead, schemaRead, workspaceRead] = await Promise.allSettled([
    readPage(`${base}/questions?lang=${locale}`, (payload) => playQuestionListSchema.parse(payload).items, policy),
    readPage(`${base}/story?lang=${locale}`, (payload) => playStorySchema.parse(payload), {
      ...policy,
      absent: ["story_not_found"],
    }),
    readPage(`${base}/log?limit=${QUERY_LOG_PAGE_SIZE}&offset=0`, (payload) => queryLogResponseSchema.parse(payload), {
      ...policy,
      bestEffort: true,
    }),
    readPage(`${base}/schema`, (payload) => gameSchemaSchema.parse(payload), {
      ...quiet,
      absent: [...TAKES_THE_SCREEN, "schema_hidden", "no_game_yet"],
    }),
    readPage(`${base}/workspace?lang=${locale}`, (payload) => workspaceSchema.parse(payload), quiet),
  ]);

  const questions = settledValue(questionsRead);
  if (questions instanceof Refusal) return { kind: "unavailable", code: questions.code };
  const story = settledValue(storyRead);
  if (story instanceof Refusal) return { kind: "unavailable", code: story.code };
  const log = settledValue(logRead);
  if (log instanceof Refusal) return { kind: "unavailable", code: log.code };

  return {
    kind: "ready",
    questions,
    story,
    log: log ? { ...log, failed: false } : { items: [], total: 0, failed: true },
    schema: settledValue(schemaRead),
    workspace: settledValue(workspaceRead),
  };
}

/** A read's value; its rejection, a required read's failure or Next's own control flow, is thrown on. */
function settledValue<T>(result: PromiseSettledResult<T>): T {
  if (result.status === "rejected") throw result.reason;
  return result.value;
}
