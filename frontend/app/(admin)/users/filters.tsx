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
 * Account filters. A GET form to `/users` with a submit button, so it works
 * without JavaScript; typing navigates rather than fetching, so the URL stays
 * the only state (shareable, Back works, reset is a link). `replace`, not
 * `push`, so one search is one history entry.
 */

/** 300ms: shorter than ~200ms does not collapse a burst; longer than ~500ms feels detached. */
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

  // Uncontrolled inputs ignore a new `defaultValue`, so sync them on Back or
  // reset. Only while unfocused: a navigation landing mid-typing would swallow
  // the letters typed since.
  useEffect(() => {
    const typing = box.current !== null && document.activeElement === box.current;

    if (box.current && !typing && box.current.value !== query) box.current.value = query;
    if (picker.current && picker.current.value !== status) picker.current.value = status;
  }, [query, status]);

  const navigate = useCallback(
    (next: { query: string; status: string }) =>
      // A new search starts at page one.
      startSearch(() =>
        router.replace(accountsHref({ ...next, resetPage: true }), { scroll: false }),
      ),
    [router],
  );

  // The select is not debounced: a choice is complete when made. Built once per
  // status (`navigate` is stable), so the pause restarts per keystroke, not per
  // render.
  const search = useMemo(
    () => debounce((next: string) => navigate({ query: next, status }), SEARCH_PAUSE_MS),
    [navigate, status],
  );

  // A timer firing after unmount would navigate somewhere unasked.
  useEffect(() => search.cancel, [search]);

  return (
    <form
      method="get"
      action="/users"
      className="flex flex-wrap items-end gap-3"
      // With JavaScript, submit is the same navigation without the wait.
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
          // Uncontrolled: a controlled value re-rendered from the server makes
          // the caret jump.
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
          {/* Empty means every account except deleted ones (`users.Filter`), so
             the label must not say "all". "deleted" comes from
             `ACCOUNT_STATUSES` below. */}
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

      {/* Announced, since the dimmed list is visual only. */}
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
