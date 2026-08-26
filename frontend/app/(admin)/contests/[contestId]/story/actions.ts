"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type StoryState = { code?: string; saved?: boolean };

/**
 * Saving the crime story.
 *
 * The whole set of languages goes in one request, because that is the shape
 * the endpoint takes and the shape the publish gate reasons about: what has to
 * be consistent is the set, and a half-applied one is exactly what the gate
 * would then have to guess at.
 *
 * A language with an empty box is dropped rather than sent as an empty string.
 * The two are different facts — "not written yet" and "deliberately blank" —
 * and only the first is true of a story an author has not got to. Sent as an
 * empty body it would satisfy the gate's presence check and publish a contest
 * whose Romanian readers get a blank page.
 */
export async function saveStoryAction(_previous: StoryState, form: FormData): Promise<StoryState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const translations: Record<string, string> = {};
  for (const [key, value] of form.entries()) {
    if (!key.startsWith("body.")) continue;
    const body = String(value).trim();
    if (body) translations[key.slice("body.".length)] = body;
  }

  const failure = await serverRequest(`/contests/${contestId}/story`, {
    method: "PUT",
    body: { translations },
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failure instanceof ApiError ? failure.code : "unreachable" };

  // The gate on the overview counts this story; so does the register's title.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}
