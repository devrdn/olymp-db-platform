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
 * This also wraps `/users/[userId]`, since a layout covers every route
 * below it in the segment — harmless there, as that screen reads no
 * selection context of its own.
 *
 * Kept to exactly this: nothing here reads `searchParams` or fetches
 * anything, so there is nothing a search could leave stale.
 */
export default function UsersLayout({ children }: { children: React.ReactNode }) {
  return <SelectionProvider>{children}</SelectionProvider>;
}
