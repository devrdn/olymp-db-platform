"use server";

import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";

import { ApiError } from "@/lib/api/client";
import { contestSchema, type Enrollment, ENROLLMENTS, enumFromForm, QUESTION_MODES, type QuestionMode, type Timing, TIMINGS } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { LOCALES, type Locale } from "@/lib/i18n/config";

export type NewContestState = { code?: string };


/**
 * Creating a contest.
 *
 * Everything here is a choice that freezes later, which is why it is asked
 * once, up front: the question format and the timing model stop being editable
 * the moment the contest starts, and the language set decides what every
 * subsequent editor asks for. The schedule and the network rules are not
 * asked, because a draft has neither and the publish gate will demand them
 * when they actually matter.
 *
 * Only the languages the interface itself ships are offered. The API validates
 * the codes against its own catalogue — a table, so a fourth language is an
 * INSERT rather than a deploy — but it publishes no endpoint to read that
 * catalogue, so the interface cannot yet offer a language it has no
 * translation file for. That is a gap on the API's side, not a decision here.
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
  // A default that is not among the chosen languages would be accepted by the
  // form and refused by a partial unique index nobody can read the message of.
  const defaultLanguage = chosen.includes(fallback) ? fallback : chosen[0];

  const translations: Record<string, { title: string; description?: string }> = {};
  for (const code of chosen) {
    const title = String(form.get(`title.${code}`) ?? "").trim();
    if (title === "") continue;

    const description = String(form.get(`description.${code}`) ?? "").trim();
    translations[code] = description ? { title, description } : { title };
  }

  // The contest is identified by its title everywhere it appears. Created
  // without one in the language it falls back to, it is a blank row in the
  // register that nobody can tell from the next blank row.
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

  // Straight into the workspace: the next thing an author does is write the
  // story, and the register would only be a stop on the way there.
  redirect(`/contests/${created.id}`);
}
