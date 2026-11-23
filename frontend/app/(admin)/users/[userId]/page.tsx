import Link from "next/link";
import { notFound } from "next/navigation";

import { Band } from "@/components/layout/band";
import { Tag } from "@/components/ui/tag";
import { accountSchema, roleListSchema } from "@/lib/api/accounts";
import { isId } from "@/lib/api/ids";
import { readPage } from "@/lib/api/page-read";
import { fetchIdentity } from "@/lib/auth/session";
import { formatMoment } from "@/lib/format/datetime";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { readMailOffer } from "../delivery";
import { AccountCard } from "./account-card";

export async function generateMetadata(props: PageProps<"/users/[userId]">) {
  const { userId } = await props.params;
  if (!isId(userId)) return {};

  const [dict, account] = await Promise.all([activeDictionary(), loadAccount(userId)]);
  return { title: `${account.fullName} · ${dict.accounts.heading}` };
}

/**
 * Loads one account, after its caller refused a non-id segment. `forbidden` is
 * answered as not found, as in the contest workspace. The viewer is fetched too
 * so a self-block is not offered; `users.Service` refuses it regardless.
 */
function loadAccount(userId: string) {
  return readPage(`/users/${userId}`, (payload) => accountSchema.parse(payload), {
    here: `/users/${userId}`,
    hideForbidden: true,
  });
}

export default async function AccountPage(props: PageProps<"/users/[userId]">) {
  const { userId } = await props.params;
  if (!isId(userId)) notFound();

  const [dict, locale, account, { items: roles }, identity, mail] = await Promise.all([
    activeDictionary(),
    activeLocale(),
    loadAccount(userId),
    readPage("/roles", (payload) => roleListSchema.parse(payload), { here: `/users/${userId}` }),
    fetchIdentity(),
    readMailOffer(),
  ]);

  const t = dict.accounts;

  // The actor's login comes joined on `account`; only the date needs formatting
  // here.
  const statusChangedAtLabel = account.statusChangedAt
    ? formatMoment(account.statusChangedAt, { locale })
    : null;

  return (
    <Band fill className="flex flex-col gap-8 py-12">
      <div className="flex flex-col gap-6">
        <Link
          href="/users"
          className="w-fit font-mono text-data text-ink-3 transition-colors duration-(--t-input) ease-standard hover:text-ink"
        >
          {t.card.back}
        </Link>

        <div className="flex min-w-0 flex-col gap-3">
          <h1 className="max-w-head text-h2 text-balance text-ink">{account.fullName}</h1>

          <div className="flex flex-wrap items-center gap-3">
            <Tag tone={account.status === "active" ? "good" : "bad"}>
              {t.status[account.status]}
            </Tag>
            {account.mustChangePassword ? <Tag tone="warn">{t.handoverPending}</Tag> : null}
            <span className="font-mono text-data text-ink-3">{account.login}</span>
            <span aria-hidden className="h-3 w-px bg-line-2" />
            <span className="font-mono text-data text-ink-3">
              {t.card.created} {formatMoment(account.createdAt, { locale })}
            </span>
          </div>
        </div>
      </div>

      <AccountCard
        account={account}
        roles={roles}
        viewerId={identity?.id ?? ""}
        dict={dict}
        statusChangedAtLabel={statusChangedAtLabel}
        locale={locale}
        mail={mail}
      />
    </Band>
  );
}
