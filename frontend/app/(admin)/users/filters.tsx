"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useCallback, useEffect, useMemo, useRef, useTransition } from "react";

import { buttonVariants } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { ACCOUNT_STATUSES } from "@/lib/api/accounts-terms";
import { debounce } from "@/lib/format/debounce";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { accountsHref } from "./search-href";

/**
 * The account filters: typing searches, and the address still holds the view.
 *
 * It stays a GET form pointed at `/users` and keeps its submit button. That is
 * not a leftover — it is what the screen does when JavaScript has not loaded
 * or has failed, and it costs one element. The live behaviour below is an
 * enhancement on top of a form that already worked.
 *
 * What typing does is navigate, not fetch. The URL remains the single piece of
 * state, so a search is shareable, the back button walks through the searches
 * somebody tried, and the reset stays an ordinary link. Holding the query in
 * component state instead would take all three away in exchange for nothing.
 *
 * `replace`, not `push`: every keystroke would otherwise become an entry, and
 * one press of Back would step back through "popesc", "popes", "pope". One
 * search is one entry.
 */

/**
 * Long enough that a typed word is one query rather than six, short enough
 * that the result feels like it belongs to the typing. Under about 200ms the
 * pause stops collapsing bursts; past about 500ms the screen feels detached
 * from the keyboard.
 */
const SEARCH_PAUSE_MS = 300;

const CONTROL = "h-(--control-h) w-full border border-edge bg-bg px-2.5 text-control text-ink";

export function AccountFilters({
  query,
  status,
  dict,
}: {
  query: string;
  status: string;
  dict: Dictionary;
}) {
  const t = dict.accounts;
  const router = useRouter();
  const [searching, startSearch] = useTransition();
  const filtered = Boolean(query || status);

  const box = useRef<HTMLInputElement>(null);
  const picker = useRef<HTMLSelectElement>(null);

  // React never updates an uncontrolled input when `defaultValue` changes, so
  // without this the back button and the reset link move the list while the
  // controls keep showing the previous filter — results for one search under a
  // box claiming another.
  //
  // Focus is what tells the two cases apart. A navigation that lands while
  // somebody is typing must not touch the box: they are two letters further on
  // than the address is, and writing it back would swallow those letters. Back
  // and the reset link both blur it first, which is exactly when following the
  // address is right.
  useEffect(() => {
    const typing = box.current !== null && document.activeElement === box.current;

    if (box.current && !typing && box.current.value !== query) box.current.value = query;
    if (picker.current && picker.current.value !== status) picker.current.value = status;
  }, [query, status]);

  const navigate = useCallback(
    (next: { query: string; status: string }) =>
      // `resetPage`: searching from page three of the previous result would
      // otherwise leave somebody on page three of a result with four rows.
      startSearch(() =>
        router.replace(accountsHref({ ...next, resetPage: true }), { scroll: false }),
      ),
    [router],
  );

  // The select is not debounced. A pause suits a value built letter by letter;
  // a choice from a list is complete the moment it is made, and waiting on it
  // is latency for nothing.
  //
  // `navigate` is stable, so this is built once per status rather than per
  // render — a debounce rebuilt on every render would restart its pause with
  // each re-render instead of with each keystroke.
  const search = useMemo(
    () => debounce((next: string) => navigate({ query: next, status }), SEARCH_PAUSE_MS),
    [navigate, status],
  );

  // A timer that fires after the screen is gone navigates somebody somewhere
  // they did not ask to go — including undoing a link they just followed.
  useEffect(() => search.cancel, [search]);

  return (
    <form
      method="get"
      action="/users"
      className="flex flex-wrap items-end gap-3"
      // The button still works with no JavaScript; with it, submitting is the
      // same navigation the typing already performs, without the wait.
      onSubmit={(event) => {
        event.preventDefault();
        search.cancel();
        navigate({ query: box.current?.value ?? "", status });
      }}
    >
      <div className="flex min-w-56 flex-1 flex-col gap-1.5">
        <Label htmlFor="account-q">{t.search}</Label>
        <input
          id="account-q"
          name="q"
          ref={box}
          // Uncontrolled, and deliberately: a controlled value re-rendered from
          // the server on every navigation is how the caret jumps to the end of
          // the word somebody is still typing.
          defaultValue={query}
          onChange={(event) => search(event.target.value)}
          className={CONTROL}
        />
      </div>

      <div className="flex w-44 flex-col gap-1.5">
        <Label htmlFor="account-status">{t.filter}</Label>
        <select
          id="account-status"
          name="status"
          defaultValue={status}
          ref={picker}
          onChange={(event) =>
            navigate({ query: box.current?.value ?? query, status: event.target.value })
          }
          className={CONTROL}
        >
          <option value="">{t.anyStatus}</option>
          {ACCOUNT_STATUSES.map((value) => (
            <option key={value} value={value}>
              {t.status[value]}
            </option>
          ))}
        </select>
      </div>

      <button type="submit" className={cn(buttonVariants({ variant: "secondary" }))}>
        {t.apply}
      </button>

      {/* Announced rather than only drawn: somebody who cannot see the list
          dim needs to be told the results are being fetched. */}
      <p role="status" aria-live="polite" className="text-small text-ink-3">
        {searching ? t.searching : ""}
      </p>

      {filtered ? (
        <Link href="/users" className={cn(buttonVariants({ variant: "quiet" }))}>
          {t.empty.reset}
        </Link>
      ) : null}
    </form>
  );
}
