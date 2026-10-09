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
 * The audit trail as a register, scanned down a column. Action codes are
 * translated from the dictionary; an untranslated code is shown raw, since
 * dropping a line would make the record lie.
 */

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-baseline";

/** Changed fields named before the rest are counted. */
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
  /** Builds another page's address, keeping the filters. */
  pageHref: (offset: number) => string;
  filtered: boolean;
  dict: Dictionary;
  locale: Locale;
}) {
  const t = dict.audit;

  if (entries.length === 0) {
    // "Nothing matches these filters" offers a reset; "nothing has happened
    // yet" does not.
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
                    /* No actor means the system acted. */
                    <span className="text-small text-ink-3 italic">{t.system}</span>
                  )}
                </td>
                <td className={cn(CELL, "text-body text-ink")}>
                  {(t.actions as Record<string, string>)[entry.action] ?? (
                    <span className="font-mono text-data" title={t.unknownAction}>
                      {entry.action}
                    </span>
                  )}
                  {/* Field names as the API spells them, in monospace. */}
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
          {/* Offsets suffice: the trail is append-only and read newest first, so
             pages do not shift. */}
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
 * Changed fields behind a native `<details>`: closed, it names the first few
 * and counts the rest; open, both values of each. No client JavaScript needed.
 * "Nothing changed" is recorded on purpose and gets no disclosure.
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

      {/* The arrow carries the direction, so no word is needed. */}
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
 * The reason for a `contest.start_blocked` entry, in the publish gate's
 * vocabulary (`workspace.gate.problems`). Unknown codes are shown raw.
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
 * The reason for an `auth.login_failed` entry (a wrong password, a blocked
 * account, a guessing sweep), from the backend's `Reason*` codes. Unknown codes
 * are shown raw.
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
 * The entry's subject, by name; the kind is only the title. An existing contest
 * is a link; a deleted one keeps its id and no invented name.
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
