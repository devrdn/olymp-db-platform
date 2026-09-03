import Link from "next/link";

import { StateView } from "@/components/product/state-view";
import { buttonVariants } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import type { Account, AccountStatus, Role } from "@/lib/api/accounts";
import { formatMoment } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The accounts of the installation, as a register.
 *
 * The same shape as the contest register and for the same reason: rows compare
 * down a column, tiles do not, and "who is blocked" or "who has not signed in
 * yet" are questions answered by scanning one column.
 *
 * Roles arrive as codes and are shown by name. The codes are what
 * authorisation works in; an administrator deciding who may do what should not
 * have to know that the system administrator is spelled `admin`. A code the
 * catalogue does not name is shown raw rather than dropped — a role added
 * while this page was open is still a role the account holds, and an empty
 * cell would say the opposite.
 */

/** How many rows a page carries. Matches the API's own default. */
export const ACCOUNTS_PAGE = 50;

const STATUS_TONE: Record<AccountStatus, "good" | "bad" | "mute"> = {
  active: "good",
  blocked: "bad",
  // Softly deleted, not merely inactive: neither "good" nor a state somebody
  // fixes by unblocking, so it gets the tone that reads as settled rather
  // than urgent.
  deleted: "mute",
};

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-baseline";

export function AccountRegister({
  accounts,
  total,
  offset,
  pageHref,
  filtered,
  roles,
  dict,
  locale,
}: {
  accounts: Account[];
  total: number;
  offset: number;
  pageHref: (offset: number) => string;
  /** Whether a filter is in force, which decides which emptiness this is. */
  filtered: boolean;
  /** The catalogue the server publishes, for turning codes into names. */
  roles: Role[];
  dict: Dictionary;
  locale: Locale;
}) {
  const t = dict.accounts;
  const nameOf = new Map(roles.map((role) => [role.code, role.name]));

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-2">
        <h1 className="text-h2 text-ink">{t.heading}</h1>
        <span className="font-mono text-data text-ink-3">
          {total} {t.countLabel}
        </span>
      </div>

      {accounts.length === 0 ? (
        <div className="border-t border-line">
          {/* Two emptinesses, and they are not the same fact. A filter that
              matched nothing has a way out; an installation with no accounts
              has a first step. Offering a reset for the second would suggest
              the emptiness was the reader's doing. */}
          <StateView
            state={
              filtered
                ? {
                    kind: "empty-filtered",
                    title: t.empty.title,
                    body: t.empty.body,
                    reset: { label: t.empty.reset, href: "/users" },
                  }
                : { kind: "empty", title: t.emptyAll.title, body: t.emptyAll.body }
            }
          />
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-lg border-collapse text-left narrow:min-w-3xl">
            <thead>
              <tr>
                <th scope="col" className={cn(HEAD, "w-10 pr-0 text-right")}>
                  {t.columns.index}
                </th>
                <th scope="col" className={HEAD}>
                  {t.columns.account}
                </th>
                <th scope="col" className={cn(HEAD, "w-40")}>
                  {t.columns.state}
                </th>
                <th scope="col" className={cn(HEAD, "w-48")}>
                  {t.columns.roles}
                </th>
                <th scope="col" className={cn(HEAD, "w-44")}>
                  {t.columns.lastSeen}
                </th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((account, index) => (
                <tr
                  key={account.id}
                  className="transition-colors duration-(--t-input) ease-standard hover:bg-panel"
                >
                  <td className={cn(CELL, "pr-0 text-right font-mono text-data text-ink-3")}>
                    {String(offset + index + 1).padStart(2, "0")}
                  </td>

                  <td className={CELL}>
                    <Link
                      href={`/users/${account.id}`}
                      className="block text-row text-ink transition-colors duration-(--t-input) ease-standard hover:text-accent"
                    >
                      {account.fullName}
                    </Link>
                    <span className="mt-1 block font-mono text-data text-ink-3">
                      {account.login}
                      {account.email ? ` · ${account.email}` : ""}
                    </span>
                  </td>

                  <td className={CELL}>
                    <span className="flex flex-wrap items-center gap-1.5">
                      <Tag tone={STATUS_TONE[account.status]}>{t.status[account.status]}</Tag>
                      {/* Handed over and not yet replaced. The administrator
                          who reset it needs to see who is still carrying
                          somebody else's password. */}
                      {account.mustChangePassword ? (
                        <Tag tone="warn">{t.handoverPending}</Tag>
                      ) : null}
                    </span>
                  </td>

                  <td className={CELL}>
                    {account.roles.length === 0 ? (
                      <span className="font-mono text-data text-ink-3">{t.noRoles}</span>
                    ) : (
                      <span className="font-mono text-data text-ink-2">
                        {account.roles.map((code) => nameOf.get(code) ?? code).join(", ")}
                      </span>
                    )}
                  </td>

                  <td className={cn(CELL, "font-mono text-data whitespace-nowrap text-ink-2")}>
                    {account.lastLoginAt ? (
                      formatMoment(account.lastLoginAt, { locale })
                    ) : (
                      <span className="text-ink-3">{t.never}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {total > ACCOUNTS_PAGE ? (
        <nav className="flex items-center gap-2.5" aria-label={t.heading}>
          {offset > 0 ? (
            <Link
              href={pageHref(Math.max(0, offset - ACCOUNTS_PAGE))}
              className={cn(buttonVariants({ variant: "quiet" }))}
            >
              {t.newerPage}
            </Link>
          ) : null}
          {offset + ACCOUNTS_PAGE < total ? (
            <Link
              href={pageHref(offset + ACCOUNTS_PAGE)}
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
