"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { CONTEST_STATUSES, type ContestStatus } from "@/lib/api/contests";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type StatusState = { code?: string; moved?: ContestStatus };

export type TitleState = { code?: string; saved?: boolean };

/**
 * The titles and descriptions, replaced as a set.
 *
 * `PUT` on the wire, a full replacement in meaning — the same reason the
 * language set is: what has to stay consistent is the whole set, and a half
 * applied one is exactly what the publish gate would have to guess about.
 * `TitleEditor` only ever shows one declared language open by default, but
 * its form still carries every one of them (the rest sit behind a
 * disclosure), so this still receives the complete set on every save.
 *
 * Lives here, at the contest's root, rather than under `settings/`: the
 * screen that edits the name moved to the workspace header, and an action is
 * filed beside whoever calls it.
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

  // The title is printed in the header, in the breadcrumb and on the
  // register behind them. All of it is now stale.
  revalidatePath("/contests");
  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}

/**
 * Moving a contest between states.
 *
 * Both values arrive from a form and both are checked before they are used:
 * the identifier because it goes into a request path, and the status because
 * sending an unrecognised one would spend a round trip to be told what this
 * process already knows.
 *
 * Which transitions are legal is not decided here. The interface offers only
 * the ones its mirrored table allows, and the API refuses the rest with
 * `invalid_transition` — including the ones the mirror has drifted on. The
 * publish gate is the same arrangement: `GET /publish-check` says what is
 * missing so the button can be disabled with a reason, and the transition
 * itself is checked again on the server, where it counts.
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

  // The state is printed in the header, in the tab row's context and on the
  // register behind them. All of it is now stale.
  revalidatePath("/contests");
  revalidatePath(`/contests/${contestId}`, "layout");

  return { moved: status as ContestStatus };
}
