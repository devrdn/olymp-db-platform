import Link from "next/link";

import { StateView } from "@/components/product/state-view";
import { buttonVariants } from "@/components/ui/button";
import {
  AUDIT_PAGE,
  blockedProblems,
  loginFailureReason,
  summariseChanges,
  type AuditEntry,
} from "@/lib/api/audit";
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

/** How many changed fields a row names before it starts counting them. */
const NAMED_FIELDS = 3;

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
              <th scope="col" className={cn(HEAD, "w-56")}>
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
                  <BlockedProblems payload={entry.payload} dict={dict} />
                  <LoginFailureReason payload={entry.payload} dict={dict} />
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
 * Which fields an entry moved, and what they moved to, behind a disclosure.
 *
 * Closed, the row names the first few fields and counts the rest: a register
 * is read by scanning down a column, and a line per field turned each row into
 * a paragraph. Open, it holds every field with both values.
 *
 * A native <details>, so this page still ships no client JavaScript for
 * reading the trail: it is keyboard-operable and announced as a disclosure
 * without a line of ours. The values were in a title attribute before, which
 * is a tooltip — invisible on a touch screen, impossible to copy, and found by
 * accident if at all.
 *
 * "Nothing changed" gets no disclosure: there is nothing under it. The server
 * records it deliberately, because a save that moved nothing is otherwise
 * indistinguishable from an edit the reader simply cannot see.
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
    return <span className="ml-2 text-small text-ink-3 italic">{label}</span>;
  }
  if (changes.length === 0) return null;

  const named = changes.slice(0, NAMED_FIELDS).map((change) => change.field).join(", ");
  const rest = changes.length - NAMED_FIELDS;

  return (
    <details className="mt-1 group">
      <summary className="cursor-pointer font-mono text-data text-ink-3 marker:text-ink-3 hover:text-ink-2">
        {rest > 0 ? `${named} +${rest}` : named}
      </summary>

      {/* A description list, because that is what this is: a field, and what
          became of it. The arrow carries the direction, so neither side needs
          a word for it in any language. */}
      <dl className="mt-1.5 flex flex-col gap-1">
        {changes.map((change) => (
          <div key={change.field} className="flex flex-wrap items-baseline gap-x-2">
            <dt className="font-mono text-data text-ink-3">{change.field}</dt>
            <dd className="flex flex-wrap items-baseline gap-x-1.5 font-mono text-data text-ink-2">
              <span className="text-ink-3 line-through decoration-ink-3/50">{change.from}</span>
              <span aria-hidden>→</span>
              <span className="text-ink">{change.to}</span>
            </dd>
          </div>
        ))}
      </dl>
    </details>
  );
}

/**
 * Why a `contest.start_blocked` entry happened.
 *
 * "A contest did not start" names the symptom; an organizer opens the trail
 * for the reason, and the reason is exactly the closed vocabulary the
 * publish gate's own screen already renders (`workspace.gate.problems` —
 * `app/(admin)/contests/[contestId]/publish-gate.tsx`'s `global` list). Reused
 * here rather than invented again: the same small dot-and-sentence bullet,
 * scaled to a register row instead of a full report. A code with no wording
 * yet is still shown, raw, in the monospace register — the same rule
 * ChangeSummary and Subject already apply, because a blocked contest with an
 * unreadable reason is no better than one with none at all.
 */
function BlockedProblems({ payload, dict }: { payload: AuditEntry["payload"]; dict: Dictionary }) {
  const codes = blockedProblems(payload);
  if (codes.length === 0) return null;

  const problems = dict.workspace.gate.problems as Record<string, string>;

  return (
    <ul className="mt-1.5 flex flex-col gap-1">
      {codes.map((code, index) => (
        <li key={`${code}-${index}`} className="flex gap-2 text-small text-ink-2">
          <span aria-hidden className="mt-1.5 size-1 shrink-0 rounded-full bg-warn" />
          <span>{problems[code] ?? <span className="font-mono text-data">{code}</span>}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * Why an `auth.login_failed` entry happened.
 *
 * "Failed to sign in" names the event; an administrator investigating an
 * incident cannot tell a mistyped password from a blocked account from a
 * sweep of guesses without knowing which — three different conversations to
 * have. The reason is the closed vocabulary `backend/internal/auth`'s
 * `Reason*` constants declare, read back the same way `BlockedProblems`
 * reads `contest.start_blocked`'s problem codes: a code with no wording yet
 * is shown raw rather than dropped, because a login-failure line with an
 * unreadable reason is no better than one with none.
 */
function LoginFailureReason({ payload, dict }: { payload: AuditEntry["payload"]; dict: Dictionary }) {
  const reason = loginFailureReason(payload);
  if (!reason) return null;

  const reasons = dict.audit.failureReasons as Record<string, string>;

  return (
    <p className="mt-1 text-small text-ink-2">
      {reasons[reason] ?? <span className="font-mono text-data">{reason}</span>}
    </p>
  );
}

/**
 * What the action was about, on one line.
 *
 * The name only. The kind was there too and doubled the height of every row
 * in the register — for a word the action beside it had already said:
 * "Changed a contest · Contest". It survives as the title, which is where it
 * earns its keep, on an action this interface has no wording for yet.
 *
 * A contest that still exists is a link, because the next thing a reader
 * wants is to open it. One that is gone keeps its identifier and no name: the
 * trail outlives what it describes, and inventing a name for something
 * deleted would be inventing a record.
 */
function Subject({ entry, dict }: { entry: AuditEntry; dict: Dictionary }) {
  if (!entry.entity) return null;

  const kind = (dict.audit.entities as Record<string, string>)[entry.entity] ?? entry.entity;

  if (!entry.entity_label) {
    return entry.entity_id ? (
      <span className="font-mono text-data text-ink-3" title={kind}>
        {entry.entity_id}
      </span>
    ) : (
      <span>{kind}</span>
    );
  }

  if (entry.entity === "contest" && entry.entity_id) {
    return (
      <Link
        href={`/contests/${entry.entity_id}`}
        title={kind}
        className="text-ink underline-offset-2 hover:underline"
      >
        {entry.entity_label}
      </Link>
    );
  }

  return (
    <span className="font-mono text-data text-ink" title={kind}>
      {entry.entity_label}
    </span>
  );
}
