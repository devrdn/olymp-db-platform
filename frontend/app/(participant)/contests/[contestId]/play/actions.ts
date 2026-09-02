"use server";

import { ApiError } from "@/lib/api/client";
import { queryResultSchema, type QueryResult } from "@/lib/api/console";
import { serverRequest } from "@/lib/api/server";

/**
 * What the console shows after a run: an answer, or why there is none.
 *
 * A discriminated shape rather than a result with an optional error, because
 * the two are different screens — a table with a red box above it invites
 * reading stale rows as if they answered the question just asked.
 */
export type ConsoleState =
  | { kind: "idle" }
  | { kind: "answer"; result: QueryResult }
  | { kind: "refused"; code: string; subject?: string };

/**
 * Runs one query as the signed-in participant.
 *
 * A server action, so the session cookie stays where it already is and no
 * token reaches the browser — the same reason every other write in this
 * interface is one.
 */
export async function runQueryAction(
  _previous: ConsoleState,
  form: FormData,
): Promise<ConsoleState> {
  const contestId = String(form.get("contestId") ?? "");
  const sql = String(form.get("sql") ?? "").trim();

  // An empty console is not a query. Sending it would spend the participant's
  // rate limit on nothing and answer with a parse error about the emptiness.
  if (!contestId || sql === "") return { kind: "idle" };

  try {
    const payload = await serverRequest(`/contests/${contestId}/query`, {
      method: "POST",
      body: { sql },
    });
    return { kind: "answer", result: queryResultSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) {
      return { kind: "refused", code: error.code, subject: error.subject };
    }
    return { kind: "refused", code: "unreachable" };
  }
}
