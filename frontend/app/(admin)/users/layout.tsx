import { fetchIdentity } from "@/lib/auth/session";

import { SelectionProvider } from "./selection";

/**
 * Carries the account selection across a search.
 *
 * `page.tsx` re-renders on every filter change — the address is the state
 * (see `search-href.ts`), and searching navigates to `/users` with new
 * `searchParams` — so a provider mounted there is recreated with it, and
 * whatever was picked before the search is gone the moment the results
 * change. A layout does not share that fate: per Next's own docs
 * (`node_modules/next/dist/docs/01-app/03-api-reference/03-file-conventions/layout.md`,
 * "Layouts do not rerender on navigation" — precisely because they have no
 * access to `searchParams`, which is exactly what this screen never asked
 * for), this component keeps its identity, and with it the `useState` that
 * `SelectionProvider` holds, across a navigation that only changes the
 * query string. `layout.test.tsx` reproduces that reconciliation directly
 * (the same layout element, a completely different `children` subtree) to
 * confirm the store instance really does survive it.
 *
 * The same durability is why `owner` is fetched here and handed to
 * `SelectionProvider`: a component that is deliberately built to keep its
 * state across a navigation is also one that could keep it across more than
 * a search, and `SelectionStore.syncOwner` is what stops a pick from
 * surviving a change of who is signed in. `identity` is read the same way
 * `AdminLayout` reads it one level up (tolerated on failure, never a
 * redirect) — this layout decorates nothing and gates nothing, so the worst
 * a server that cannot be asked costs is a selection scoped to `null`
 * instead of a real id, not a broken page.
 *
 * This also wraps `/users/[userId]`, since a layout covers every route
 * below it in the segment — harmless there, as that screen reads no
 * selection context of its own.
 */
export default async function UsersLayout({ children }: { children: React.ReactNode }) {
  const identity = await fetchIdentity().catch(() => null);
  return <SelectionProvider owner={identity?.id ?? null}>{children}</SelectionProvider>;
}
