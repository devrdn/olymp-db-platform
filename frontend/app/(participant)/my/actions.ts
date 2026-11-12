"use server";

import { revalidatePath } from "next/cache";

import { fetchAuthMethods } from "@/lib/api/auth-methods";
import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type EnrollState = {
  code?: string;
  enrolled?: boolean;
  /** With `contest_trusted_email_required`: the site's domain, when it has one. */
  domain?: string;
  /**
   * With `contest_trusted_email_required`: a reset link can confirm an
   * address the account already holds, since recovery is on.
   */
  confirmByReset?: boolean;
};

/**
 * Enrols the signed-in participant in a contest. The id comes from the form
 * and goes into a request path, so it is validated first: a slash or `..`
 * would address a different endpoint, which the API would not refuse.
 *
 * The contest's own rules (enrolment closed, deadline passed, address
 * outside the network) are the API's to decide and come back as codes. The
 * network check could not be done here anyway: only the trusted proxy knows
 * the address that counts.
 */
export async function enrollAction(_previous: EnrollState, form: FormData): Promise<EnrollState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const failure = await serverRequest(`/contests/${contestId}/enroll`, { method: "POST" }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    const code = failureCode(failure);

    // A double click or a stale list: either way the account is enrolled,
    // which is what it asked for.
    if (code === "already_enrolled") return { enrolled: true };

    // The refusal's sentence cannot name the domain, so it is asked for here,
    // only on this refusal, rather than on every load of the register.
    if (code === "contest_trusted_email_required") {
      const methods = await fetchAuthMethods();
      const domain = methods?.trustedEmailDomain;
      return {
        code,
        ...(domain ? { domain } : {}),
        ...(methods?.passwordReset ? { confirmByReset: true } : {}),
      };
    }

    return { code };
  }

  // The listing is session-scoped, so the new contest appears only after
  // a fresh request.
  revalidatePath("/my");

  return { enrolled: true };
}
