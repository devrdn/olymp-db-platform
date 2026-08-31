import Link from "next/link";

import { StateView } from "@/components/product/state-view";
import { buttonVariants } from "@/components/ui/button";
import { AUDIT_PAGE, summariseChanges, type AuditEntry } from "@/lib/api/audit";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The audit trail, as a register.
 *
 * Same shape as the contest list and for the same reason: the data is tabular
 * and the whole use of it is scanning down a column — every login from one
 * address, everything one person did this morning. A wall of cards cannot be
 * scanned that way.
 *
 * The action arrives as a machine code and is translated here, from the
 * dictionary, exactly as an error code is. A code with no wording yet is shown
 * as the code rather than hidden: the trail is a record, and dropping a line
 * from it because the interface has not caught up would make the record lie.
 */

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-baseline";

export function AuditTrailRegister({
  entries,
  total,
  offset,
  pageHref,
  filtered,
  dict,
  locale,
}: {
  entries: AuditEntry[];
  total: number;
  offset: number;
  /** Builds the address of another page, so paging keeps the filters. */
  pageHref: (offset: number) => string;
  filtered: boolean;
  dict: Dictionary;
  locale: Locale;
}) {
  const t = dict.audit;

  if (entries.length === 0) {
    // Two different facts. "Nothing matches these filters" offers the way out;
    // "nothing has happened yet" is not a problem and has no reset to offer.
    return (
      <StateView
        state={
          filtered
            ? {
                kind: "empty-filtered",
                title: t.empty,
                body: t.lede,
                reset: { label: t.reset, href: "/audit" },
              }
            : { kind: "empty", title: t.emptyAll, body: t.lede }
        }
      />
    );
  }

  const timestamp = new Intl.DateTimeFormat(locale, {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: "UTC",
  });

  return (
    <div className="flex flex-col gap-6">
      <p className="font-mono text-data text-ink-3">{t.counted.replace("{n}", String(total))}</p>

      <div className="overflow-x-auto">
        <table className="w-full min-w-lg border-collapse text-left">
          <thead>
            <tr>
              <th scope="col" className={cn(HEAD, "w-44")}>
                {t.columns.when}
              </th>
              <th scope="col" className={cn(HEAD, "w-40")}>
                {t.columns.who}
              </th>
              <th scope="col" className={HEAD}>
                {t.columns.what}
              </th>
              <th scope="col" className={cn(HEAD, "w-36")}>
                {t.columns.about}
              </th>
              <th scope="col" className={cn(HEAD, "w-32")}>
                {t.columns.where}
              </th>
            </tr>
          </thead>
          <tbody>
            {entries.map((entry) => (
              <tr key={entry.id}>
                <td className={cn(CELL, "font-mono text-data text-ink-2 whitespace-nowrap")}>
                  {timestamp.format(new Date(entry.created_at))}
                </td>
                <td className={CELL}>
                  {entry.actor_login ? (
                    <span className="font-mono text-data text-ink">{entry.actor_login}</span>
                  ) : (
                    /* Not a gap: an entry with no actor is the system acting,
                       and saying so is more honest than an empty cell that
                       reads as missing data. */
                    <span className="text-small text-ink-3 italic">{t.system}</span>
                  )}
                </td>
                <td className={cn(CELL, "text-body text-ink")}>
                  {(t.actions as Record<string, string>)[entry.action] ?? (
                    <span className="font-mono text-data" title={t.unknownAction}>
                      {entry.action}
                    </span>
                  )}
                  {/* What moved, under what it was called. In the monospace
                      register, where the interface's own prose ends and the
                      record's raw data begins — these are field names as the
                      API spells them, not sentences. */}
                  <ChangeSummary payload={entry.payload} label={t.unchanged} />
                </td>
                <td className={cn(CELL, "text-small text-ink-2")}>
                  <Subject entry={entry} dict={dict} />
                </td>
                <td className={cn(CELL, "font-mono text-data text-ink-3")}>{entry.ip}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {total > AUDIT_PAGE ? (
        <nav className="flex items-center gap-2.5" aria-label={t.heading}>
          {/* Offsets rather than a cursor: the trail is append-only and read
              newest first, so a page cannot shift under a reader the way it
              would in a table that is edited. */}
          {offset > 0 ? (
            <Link
              href={pageHref(Math.max(0, offset - AUDIT_PAGE))}
              className={cn(buttonVariants({ variant: "quiet" }))}
            >
              {t.newerPage}
            </Link>
          ) : null}
          {offset + AUDIT_PAGE < total ? (
            <Link
              href={pageHref(offset + AUDIT_PAGE)}
              className={cn(buttonVariants({ variant: "quiet" }))}
            >
              {t.olderPage}
            </Link>
          ) : null}
        </nav>
      ) : null}
    </div>
  );
}

/**
 * The fields an entry moved, beneath the action that moved them.
 *
 * A second line rather than a sixth column: a change set is a list, and a
 * column wide enough for one would squeeze the four that a reader scans.
 *
 * "Nothing changed" is shown, not hidden. The server records it deliberately,
 * because a save that moved nothing is otherwise indistinguishable from an
 * edit the reader simply cannot see.
 */
function ChangeSummary({
  payload,
  label,
}: {
  payload: AuditEntry["payload"];
  label: string;
}) {
  const { changes, unchanged } = summariseChanges(payload);

  if (unchanged) {
    return <p className="pt-1 text-small text-ink-3 italic">{label}</p>;
  }
  if (changes.length === 0) return null;

  return (
    <ul className="flex flex-col gap-0.5 pt-1">
      {changes.map((change) => (
        <li key={change.field} className="font-mono text-data text-ink-2">
          <span className="text-ink-3">{change.field}</span> {change.from} → {change.to}
        </li>
      ))}
    </ul>
  );
}

/**
 * What the action was about — by name where there is one.
 *
 * The type alone ("Contest") answers half the question and leaves out the
 * half that matters. A contest that still exists is a link, because the next
 * thing a reader wants is to look at it; one that is gone keeps its
 * identifier, which is all that honestly remains of it.
 */
function Subject({ entry, dict }: { entry: AuditEntry; dict: Dictionary }) {
  if (!entry.entity) return null;

  const kind = (dict.audit.entities as Record<string, string>)[entry.entity] ?? entry.entity;

  if (!entry.entity_label) {
    return (
      <span className="flex flex-col gap-0.5">
        <span>{kind}</span>
        {entry.entity_id ? (
          /* No name means the thing is gone. The identifier is not decoration
             here: it is the only handle left on what the entry describes. */
          <span className="font-mono text-data text-ink-3">{entry.entity_id}</span>
        ) : null}
      </span>
    );
  }

  const name =
    entry.entity === "contest" && entry.entity_id ? (
      <Link href={`/contests/${entry.entity_id}`} className="text-ink underline-offset-2 hover:underline">
        {entry.entity_label}
      </Link>
    ) : (
      <span className="font-mono text-data text-ink">{entry.entity_label}</span>
    );

  return (
    <span className="flex flex-col gap-0.5">
      <span>{kind}</span>
      {name}
    </span>
  );
}
