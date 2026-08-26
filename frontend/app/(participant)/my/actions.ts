"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type EnrollState = { code?: string; enrolled?: boolean };

/**
 * Signing oneself up for a contest.
 *
 * The identifier arrives in the form, which makes it a value a visitor
 * controls, and it goes straight into a request path. It is checked before it
 * gets there: `/contests/${id}/enroll` with a slash in `id` addresses a
 * different endpoint entirely, and `fetch` resolves the `..` away before the
 * request leaves, so nothing downstream could tell. The API would refuse an
 * unknown contest anyway; it would not refuse a well-formed request to the
 * wrong endpoint.
 *
 * Everything the contest's own rules say — enrolment closed, the deadline
 * passed, the address outside the university network — is decided by the API
 * and comes back as a code this form reports. None of it is re-implemented
 * here, and the network check in particular could not be: the address that
 * counts is the one the trusted proxy reports, which this process is not in a
 * position to know.
 */
export async function enrollAction(_previous: EnrollState, form: FormData): Promise<EnrollState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const failure = await serverRequest(`/contests/${contestId}/enroll`, { method: "POST" }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    const code = failure instanceof ApiError ? failure.code : "unreachable";

    // Two accepted answers wearing an error's clothes: a double-clicked button
    // and a list that was already stale when it rendered. Both mean the
    // account is enrolled, which is what it asked for.
    if (code === "already_enrolled") return { enrolled: true };

    return { code };
  }

  // The listing is rendered from the session's own scope, so the newly joined
  // contest only appears once the server has been asked again.
  revalidatePath("/my");

  return { enrolled: true };
}
