"use client";

import { useId } from "react";

import { EMAIL_SKIP_REASONS } from "@/lib/api/accounts-terms";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { plural } from "@/lib/i18n/plural";

/**
 * Emailing sign-in details beside an issued one-time password: the option on
 * each form that issues one, and what it did once the passwords are on screen.
 * The passwords themselves are always shown by the form; this only adds to
 * them.
 */

/**
 * What the page learnt about mail (`readMailOffer` in `delivery.ts`):
 * `unknown` when the status could not be read.
 */
export type MailOffer =
  | { status: "on"; perMinute: number }
  | { status: "off" }
  | { status: "unknown" };

/** The outbox's rate for `EmailSummary`; 0 when it is not known. */
export function sendingRate(mail: MailOffer): number {
  return mail.status === "on" ? mail.perMinute : 0;
}

/**
 * The option while mail is on, ticked by default; a notice instead when the
 * status could not be read, so a missing option is never a mystery; nothing
 * while mail is off. Submitted as `send_by_email=on`, which `sendByEmail` in
 * `delivery.ts` reads; unticked, the request asks for nothing.
 */
export function SendByEmailOption({ mail, dict }: { mail: MailOffer; dict: Dictionary }) {
  const t = dict.accounts.signInMail;
  const hintId = useId();

  if (mail.status === "off") return null;
  if (mail.status === "unknown") {
    return <p className="max-w-body text-small text-warn">{t.unknown}</p>;
  }

  return (
    <div className="flex flex-col gap-1.5">
      <label className="flex items-center gap-2.5 text-control text-ink">
        <input
          type="checkbox"
          name="send_by_email"
          defaultChecked
          aria-describedby={hintId}
          className="size-4 accent-cta"
        />
        {t.option}
      </label>
      <p id={hintId} className="max-w-body pl-6.5 text-small text-ink-2">
        {t.optionHint}
      </p>
    </div>
  );
}

/** One issued password's email outcome, by the login it was issued to. */
export type EmailRow = { login: string; emailed?: boolean; reason?: string };

export type EmailSummaryData = {
  queued: number;
  notEmailed: number;
  groups: { reason: string; logins: string[] }[];
};

/**
 * Counts the queued messages and groups the rest by reason, known reasons
 * first. Null when email was not asked for: then no row is emailed and none
 * carries a reason.
 */
export function summariseEmail(rows: readonly EmailRow[]): EmailSummaryData | null {
  let queued = 0;
  const byReason = new Map<string, string[]>();

  for (const row of rows) {
    if (row.emailed) {
      queued++;
    } else if (row.reason) {
      const logins = byReason.get(row.reason);
      if (logins) logins.push(row.login);
      else byReason.set(row.reason, [row.login]);
    }
  }

  if (queued === 0 && byReason.size === 0) return null;

  const known: readonly string[] = EMAIL_SKIP_REASONS;
  const rank = (reason: string) => {
    const index = known.indexOf(reason);
    return index === -1 ? known.length : index;
  };
  const groups = [...byReason]
    .map(([reason, logins]) => ({ reason, logins }))
    .sort((a, b) => rank(a.reason) - rank(b.reason));

  const notEmailed = groups.reduce((sum, group) => sum + group.logins.length, 0);
  return { queued, notEmailed, groups };
}

/**
 * How long the queued messages take at the outbox's rate: whole minutes,
 * rounded up, and at least one. Messages already waiting make it longer, which
 * the sentence says.
 */
export function sendingTime(
  queued: number,
  perMinute: number,
  dict: Dictionary,
  locale: Locale,
): string {
  const t = dict.accounts.signInMail;
  const minutes = plural(t.minutes, Math.max(1, Math.ceil(queued / perMinute)), locale);
  return (queued === 1 ? t.etaOne : t.etaMany).replace("{minutes}", minutes);
}

/** Up to this many accounts not emailed, their logins show without a click. */
const OPEN_UP_TO = 10;

/**
 * How many messages were queued, and every account not emailed by login under
 * its reason's sentence. A long list starts collapsed, a reason this build
 * does not name is shown raw, and nothing renders when email was not asked
 * for.
 */
export function EmailSummary({
  rows,
  perMinute,
  dict,
  locale,
}: {
  rows: readonly EmailRow[];
  /** The outbox's sending rate as the page read it; 0 leaves the time out. */
  perMinute: number;
  dict: Dictionary;
  locale: Locale;
}) {
  const summary = summariseEmail(rows);
  if (!summary) return null;

  const t = dict.accounts.signInMail;
  const reasons = dict.accounts.emailSkipped as Record<string, string>;
  const open = summary.notEmailed <= OPEN_UP_TO;

  return (
    <div className="flex flex-col gap-2 border-t border-line pt-3">
      {summary.queued > 0 ? (
        <div className="flex flex-col gap-1">
          <p className="text-small text-good">{plural(t.queued, summary.queued, locale)}</p>
          <p className="max-w-body text-small text-ink-2">
            {perMinute > 0 ? `${sendingTime(summary.queued, perMinute, dict, locale)} ` : null}
            {t.replaced}
          </p>
        </div>
      ) : null}

      {summary.notEmailed > 0 ? (
        <>
          <p className="text-small text-warn">
            {t.notEmailed.replace("{n}", String(summary.notEmailed))}
          </p>
          {summary.groups.map((group) => (
            <details key={group.reason} open={open}>
              <summary className="cursor-pointer text-small text-ink-2 marker:text-ink-3 hover:text-ink">
                {t.group
                  .replace("{n}", String(group.logins.length))
                  .replace("{reason}", reasons[group.reason] ?? group.reason)}
              </summary>
              <ul className="mt-1.5 flex max-h-40 flex-wrap gap-x-3 gap-y-1 overflow-y-auto font-mono text-data text-ink-2">
                {group.logins.map((login, index) => (
                  <li key={`${login}-${index}`}>{login}</li>
                ))}
              </ul>
            </details>
          ))}
        </>
      ) : null}
    </div>
  );
}
