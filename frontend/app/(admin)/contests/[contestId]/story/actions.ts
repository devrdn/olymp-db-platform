"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { cleanEditorMarkdown } from "@/lib/format/markdown";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type StoryState = { code?: string; saved?: boolean };

/**
 * Saves the story for every language in one request, the set the endpoint
 * replaces and the publish gate checks. An empty box is dropped rather than
 * sent: an empty body would pass the gate's presence check.
 */
export async function saveStoryAction(_previous: StoryState, form: FormData): Promise<StoryState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const translations: Record<string, string> = {};
  for (const [key, value] of form.entries()) {
    if (!key.startsWith("body.")) continue;
    // Cleaned before storing too: the editor writes an empty paragraph as a
    // literal `<br />`, and exports carry the stored text.
    const body = cleanEditorMarkdown(String(value)).trim();
    if (body) translations[key.slice("body.".length)] = body;
  }

  const failure = await serverRequest(`/contests/${contestId}/story`, {
    method: "PUT",
    body: { translations },
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  // The publish gate counts the story.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}
