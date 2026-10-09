"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { CONTEST_STATUSES, type ContestStatus } from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type StatusState = { code?: string; moved?: ContestStatus };

export type TitleState = { code?: string; saved?: boolean };

/**
 * Replaces titles and descriptions as a set, so the publish gate never sees a
 * half-applied one. `TitleEditor` submits every language, collapsed ones
 * included.
 */
export async function saveTranslationsAction(
  _previous: TitleState,
  form: FormData,
): Promise<TitleState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const translations: Record<string, { title: string; description?: string }> = {};

  for (const [key, value] of form.entries()) {
    if (!key.startsWith("title.")) continue;
    const title = String(value).trim();
    if (!title) continue;

    const lang = key.slice("title.".length);
    const description = String(form.get(`description.${lang}`) ?? "").trim();
    translations[lang] = description ? { title, description } : { title };
  }

  const failure = await serverRequest(`/contests/${contestId}/translations`, {
    method: "PUT",
    body: { translations },
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  // The title also appears in the header, breadcrumb and register.
  revalidatePath("/contests");
  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}

/**
 * Changes a contest's status. Both values are validated first: the id goes into
 * a path, and an unknown status would waste a round trip. Legality is decided
 * by the API (`invalid_transition`); the interface only offers what its
 * mirrored table allows.
 */
export async function setStatusAction(
  _previous: StatusState,
  form: FormData,
): Promise<StatusState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const status = String(form.get("status") ?? "");
  if (!CONTEST_STATUSES.includes(status as ContestStatus)) return { code: "invalid_request" };

  const failure = await serverRequest(`/contests/${contestId}/status`, {
    method: "POST",
    body: { status },
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  // The status also appears in the header and register.
  revalidatePath("/contests");
  revalidatePath(`/contests/${contestId}`, "layout");

  return { moved: status as ContestStatus };
}
