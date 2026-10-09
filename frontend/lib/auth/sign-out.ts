/**
 * Ending a session in both places: the API withdraws the server-side session,
 * and this side clears the cookie it wrote, which the API's `Set-Cookie`
 * cannot reach from a server-to-server call. Injected for testing.
 */

export type SignOutDeps = {
  endSession: () => Promise<unknown>;
  clearCookie: () => void;
};

export async function signOut({ endSession, clearCookie }: SignOutDeps): Promise<void> {
  // If the API is unreachable, the cookie is still cleared: the token then
  // lapses on its own, the same exposure as closing the tab.
  await endSession().catch(() => undefined);

  clearCookie();
}
