"use server";

import { CONTEST_STATUSES, type ContestStatus } from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { serverAction, serverRequest, type ActionState } from "@/lib/api/server";

import { contestChanged } from "./revalidation";

export type StatusState = ActionState & { moved?: ContestStatus };

export type TitleState = ActionState;

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

  return serverAction(
    () => serverRequest(`/contests/${contestId}/translations`, { method: "PUT", body: { translations } }),
    { revalidate: contestChanged(contestId, { register: true }) },
  );
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

  return serverAction(() => serverRequest(`/contests/${contestId}/status`, { method: "POST", body: { status } }), {
    revalidate: contestChanged(contestId, { register: true }),
    success: () => ({ ok: true, moved: status as ContestStatus }),
  });
}
