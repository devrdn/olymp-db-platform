"use server";

import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";

import { ApiError } from "@/lib/api/client";
import { contestSchema, type Enrollment, ENROLLMENTS, enumFromForm, QUESTION_MODES, type QuestionMode, type Timing, TIMINGS } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { LOCALES, type Locale } from "@/lib/i18n/config";

export type NewContestState = { code?: string };


/**
 * Creates a contest with the choices that freeze later: question format, timing
 * model and languages. Only languages the interface ships are offered, since
 * the API has no endpoint to read its catalogue.
 */
export async function createContestAction(
  _previous: NewContestState,
  form: FormData,
): Promise<NewContestState> {
  const chosen = form
    .getAll("languages")
    .filter((code): code is Locale => (LOCALES as readonly string[]).includes(String(code)));

  if (chosen.length === 0) return { code: "invalid_request" };

  const fallback = enumFromForm(form.get("defaultLanguage"), LOCALES) ?? chosen[0];
  // A default outside the chosen set would hit a partial unique index with an
  // unreadable error.
  const defaultLanguage = chosen.includes(fallback) ? fallback : chosen[0];

  const translations: Record<string, { title: string; description?: string }> = {};
  for (const code of chosen) {
    const title = String(form.get(`title.${code}`) ?? "").trim();
    if (title === "") continue;

    const description = String(form.get(`description.${code}`) ?? "").trim();
    translations[code] = description ? { title, description } : { title };
  }

  // A title in the default language is required, or the register shows an
  // indistinguishable blank row.
  if (!translations[defaultLanguage]) return { code: "invalid_request" };

  const timing = enumFromForm<Timing>(form.get("timing"), TIMINGS) ?? "fixed";
  const duration = Number(form.get("durationMin"));
  const durationMin =
    timing === "individual" && Number.isFinite(duration) && duration > 0
      ? Math.floor(duration)
      : null;

  if (timing === "individual" && durationMin === null) return { code: "invalid_request" };

  const created = await serverRequest("/contests", {
    method: "POST",
    body: {
      enrollment: enumFromForm<Enrollment>(form.get("enrollment"), ENROLLMENTS) ?? "invite_only",
      question_mode: enumFromForm<QuestionMode>(form.get("questionMode"), QUESTION_MODES) ?? "multi",
      timing,
      duration_min: durationMin,
      starts_at: null,
      ends_at: null,
      allowed_cidrs: [],
      languages: chosen.map((code) => ({ code, is_default: code === defaultLanguage })),
      translations,
    },
  }).then(
    (payload) => contestSchema.parse(payload),
    (error: unknown) => (error instanceof ApiError ? error : null),
  );

  if (created instanceof ApiError) return { code: created.code };
  if (created === null) return { code: "unreachable" };

  revalidatePath("/contests");

  // Straight to the workspace, where the author writes the story next.
  redirect(`/contests/${created.id}`);
}
