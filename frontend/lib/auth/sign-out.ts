/**
 * Ending a session, from this side and the server's.
 *
 * Two things have to happen and they live in different places. The session
 * itself is server-side state — a record in the shared cache — so only the API
 * can withdraw it, and until it does, anyone holding the token still has an
 * account. Our own copy of the cookie was written by the sign-in action, in
 * this application's own configuration, and the API's `Set-Cookie` cannot
 * reach it from a server-to-server call.
 *
 * Both are injected so the order and the failure behaviour are testable
 * without a framework and without a live API, exactly as sign-in is.
 */

export type SignOutDeps = {
  /** Withdraws the session server-side. */
  endSession: () => Promise<unknown>;
  /** Removes this browser's copy of it. */
  clearCookie: () => void;
};

export async function signOut({ endSession, clearCookie }: SignOutDeps): Promise<void> {
  // A failure is swallowed on purpose. The worst outcome for a control marked
  // "sign out" is remaining signed in, and if the API cannot be reached, the
  // cookie is the half we can still end. The token stays valid server-side
  // until it lapses, which is the same exposure as closing the tab — while
  // keeping the cookie would leave the visitor looking signed in and being
  // refused on every page.
  await endSession().catch(() => undefined);

  clearCookie();
}
