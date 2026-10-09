import { fetchIdentity } from "@/lib/auth/session";

import { SelectionProvider } from "./selection";

/**
 * Keeps the account selection across a search. A search re-renders `page.tsx`
 * with new `searchParams`, but a layout keeps its identity (Next: "Layouts do
 * not rerender on navigation"), so `SelectionProvider`'s state survives;
 * `layout.test.tsx` checks this.
 *
 * The same durability is why `owner` is passed: `SelectionStore.syncOwner`
 * clears picks when the signed-in administrator changes. Identity is read
 * tolerantly, as `AdminLayout` does; a failure only scopes the selection to
 * `null`. Also wraps `/users/[userId]`, which reads no selection.
 */
export default async function UsersLayout({ children }: { children: React.ReactNode }) {
  const identity = await fetchIdentity().catch(() => null);
  return <SelectionProvider owner={identity?.id ?? null}>{children}</SelectionProvider>;
}
