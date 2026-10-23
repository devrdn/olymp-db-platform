"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { cleanEditorMarkdown } from "@/lib/format/markdown";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

/** What a save of one per-language document reports back to its form. */
export type TranslationsState = { code?: string; saved?: boolean };
export type StoryState = TranslationsState;
export type SchemaNotesState = TranslationsState;

/**
 * The boxes named `{prefix}.{lang}`, as the set an endpoint replaces. An empty
 * box is dropped rather than sent: that language then has no text. Cleaned
 * before storing too: the editor writes an empty paragraph as a literal
 * `<br />`, and exports carry the stored text.
 */
function translationsFrom(form: FormData, prefix: string): Record<string, string> {
  const translations: Record<string, string> = {};
  for (const [key, value] of form.entries()) {
    if (!key.startsWith(`${prefix}.`)) continue;
    const body = cleanEditorMarkdown(String(value)).trim();
    if (body) translations[key.slice(prefix.length + 1)] = body;
  }
  return translations;
}

/** PUTs the set to `/contests/{id}{path}`; `revalidate` runs only on success. */
async function saveTranslations(
  form: FormData,
  prefix: string,
  path: string,
  revalidate: (contestId: string) => void,
): Promise<TranslationsState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const failure = await serverRequest(`/contests/${contestId}${path}`, {
    method: "PUT",
    body: { translations: translationsFrom(form, prefix) },
  }).then(
    () => null,
    (error: unknown) => error,
  );
  if (failure) return { code: failureCode(failure) };

  revalidate(contestId);
  return { saved: true };
}

/**
 * Saves the story for every language in one request, the set the endpoint
 * replaces and the publish gate checks. An empty box is dropped, since an
 * empty body would pass the gate's presence check.
 */
export async function saveStoryAction(_previous: StoryState, form: FormData): Promise<StoryState> {
  // The publish gate counts the story.
  return saveTranslations(form, "body", "/story", (contestId) => revalidatePath(`/contests/${contestId}`, "layout"));
}

/**
 * Saves the table descriptions for every language in one request. A language
 * left empty has none, and a participant reading it falls back to the default
 * language's.
 */
export async function saveSchemaNotesAction(_previous: SchemaNotesState, form: FormData): Promise<SchemaNotesState> {
  return saveTranslations(form, "schemaNotes", "/schema-notes", (contestId) =>
    revalidatePath(`/contests/${contestId}/story`),
  );
}
