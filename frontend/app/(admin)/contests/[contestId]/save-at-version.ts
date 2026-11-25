import { CONTEST_VERSION_HEADER, versionFromForm } from "@/lib/api/contests";
import { serverOutcome, serverRequest, type ActionState } from "@/lib/api/server";

import { contestChanged } from "./revalidation";

type Request = { method: string; body?: Record<string, unknown> };

type Options = {
  /** Also revalidates the contest register, for a save that changes what it lists. */
  register?: boolean;
  /**
   * Where `expected_version` goes: the body, or the query string for a DELETE,
   * whose body a proxy may drop and the guard would then fail open.
   */
  versionIn?: "body" | "query";
  /** Keeps the API's message as `detail`, for refusals that name the part at fault. */
  detail?: boolean;
};

/**
 * A save of the contest or its content from a form rendered at one version of
 * it. The version the form carries (ActionForm's `version`, which an edited
 * draft keeps) goes back as `expected_version`, so the server refuses the save
 * as `contest_changed` once the contest moved since; a form that carries no
 * readable version is refused here. Once saved, the workspace is re-read, and
 * the answer carries the version the API says the save stored
 * (`CONTEST_VERSION_HEADER`), as the state's `version`.
 */
export async function saveAtVersion(
  contestId: string,
  form: FormData,
  path: string,
  init: Request,
  { register = false, versionIn = "body", detail = false }: Options = {},
): Promise<ActionState & { detail?: string }> {
  const expectedVersion = versionFromForm(form.get("expectedVersion"));
  if (expectedVersion === undefined) return { code: "invalid_request" };

  const request =
    versionIn === "query"
      ? { path: `${path}?expected_version=${expectedVersion}`, init }
      : { path, init: { method: init.method, body: { ...init.body, expected_version: expectedVersion } } };
  let stored: number | undefined;
  const onHeaders = (headers: Headers) => {
    stored = versionFromForm(headers.get(CONTEST_VERSION_HEADER));
  };
  const result = await serverOutcome(() => serverRequest(request.path, { ...request.init, onHeaders }), {
    revalidate: contestChanged(contestId, { register }),
  });
  if (result.ok) return stored === undefined ? { ok: true } : { ok: true, version: stored };
  return detail && result.detail !== undefined ? { code: result.code, detail: result.detail } : { code: result.code };
}
