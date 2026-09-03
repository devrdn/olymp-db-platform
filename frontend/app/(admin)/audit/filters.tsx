import Link from "next/link";

import { buttonVariants } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The filters, as a plain form that navigates.
 *
 * A GET form, so what is being looked at lives in the address: the view is
 * shareable, the browser's back button steps through it, and the reset is an
 * ordinary link rather than state somebody has to remember to clear. Reading
 * the trail involves no JavaScript at all.
 *
 * These four and not a free-text search, because each one narrows an index the
 * table already carries. A search across a year of history would be a table
 * scan on the largest table in the database, run from a screen an
 * administrator leaves open.
 */

/* The controls are laid out here rather than through Field, which types its
   child as an input and would fight a select for no gain. */
const CONTROL = "h-(--control-h) w-full border border-edge bg-bg px-2.5 text-control text-ink";

export function AuditFilters({
  action,
  entity,
  from,
  to,
  actions,
  dict,
}: {
  action: string;
  entity: string;
  from: string;
  to: string;
  /**
   * Every action this installation can record, fetched from the server
   * rather than scraped off the current page — see the note on AuditPage.
   */
  actions: string[];
  dict: Dictionary;
}) {
  const t = dict.audit;
  const filtered = Boolean(action || entity || from || to);

  return (
    <form method="get" action="/audit" className="flex flex-wrap items-end gap-3">
      <div className="flex min-w-56 flex-col gap-1.5">
        <Label htmlFor="action">{t.filters.action}</Label>
        <select id="action" name="action" defaultValue={action} className={CONTROL}>
          <option value="">{t.filters.anyAction}</option>
          {actions.map((code) => (
            <option key={code} value={code}>
              {(t.actions as Record<string, string>)[code] ?? code}
            </option>
          ))}
        </select>
      </div>

      <div className="flex min-w-44 flex-col gap-1.5">
        <Label htmlFor="entity">{t.filters.entity}</Label>
        <select id="entity" name="entity" defaultValue={entity} className={CONTROL}>
          <option value="">{t.filters.anyEntity}</option>
          {Object.entries(t.entities).map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor="from">{t.filters.from}</Label>
        <input id="from" name="from" type="date" defaultValue={from} className={CONTROL} />
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor="to">{t.filters.to}</Label>
        <input id="to" name="to" type="date" defaultValue={to} className={CONTROL} />
      </div>

      <button type="submit" className={cn(buttonVariants({ variant: "secondary" }))}>
        {t.filters.apply}
      </button>

      {filtered ? (
        <Link href="/audit" className={cn(buttonVariants({ variant: "quiet" }))}>
          {t.reset}
        </Link>
      ) : null}
    </form>
  );
}
