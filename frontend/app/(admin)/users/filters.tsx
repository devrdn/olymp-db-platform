import Link from "next/link";

import { buttonVariants } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { ACCOUNT_STATUSES } from "@/lib/api/accounts";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The account filters, as a plain form that navigates.
 *
 * A GET form for the same reasons the audit trail's is one: what is being
 * looked at lives in the address, so the view is shareable, the back button
 * steps through it, and the reset is an ordinary link rather than state
 * somebody has to remember to clear. No JavaScript is involved in finding
 * somebody.
 *
 * A free-text search is offered here and deliberately not on the audit trail:
 * this table holds hundreds of rows and the query matches login, name and
 * email, which is exactly how an administrator looks for a person. The trail
 * holds a year of history, where the same control would be a table scan.
 */
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
  const filtered = Boolean(query || status);

  return (
    <form method="get" action="/users" className="flex flex-wrap items-end gap-3">
      <div className="flex min-w-56 flex-1 flex-col gap-1.5">
        <Label htmlFor="account-q">{t.search}</Label>
        <input id="account-q" name="q" defaultValue={query} className={CONTROL} />
      </div>

      <div className="flex w-44 flex-col gap-1.5">
        <Label htmlFor="account-status">{t.filter}</Label>
        <select id="account-status" name="status" defaultValue={status} className={CONTROL}>
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

      {/* Only when there is something to clear: a reset beside untouched
          filters is a control that does nothing. */}
      {filtered ? (
        <Link href="/users" className={cn(buttonVariants({ variant: "quiet" }))}>
          {t.empty.reset}
        </Link>
      ) : null}
    </form>
  );
}
