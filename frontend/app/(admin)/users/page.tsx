import { Band } from "@/components/layout/band";
import { accountListSchema, roleListSchema } from "@/lib/api/accounts";
import { ACCOUNTS_PAGE } from "@/lib/api/accounts-terms";
import { readPage } from "@/lib/api/page-read";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { AccountRegister } from "./account-register";
import { readMailOffer } from "./delivery";
import { AccountFilters } from "./filters";
import { accountsHref } from "./search-href";
import { SelectionBar } from "./selection";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.accounts.heading };
}

/**
 * Account management. A Server Component, so no token reaches the browser and
 * the first paint has rows. Filters live in the URL. The role catalogue is
 * fetched, not baked in, since roles are data.
 */
export default async function UsersPage(props: PageProps<"/users">) {
  const [params, locale, dict] = await Promise.all([
    props.searchParams,
    activeLocale(),
    activeDictionary(),
  ]);
  // Started only once the request is known, so a prerender that stops above
  // leaves no read behind. Rejects only with Next's own control flow, so it can
  // run beside the reads below and be awaited last.
  const mailRead = readMailOffer();

  const text = (key: string) => (typeof params[key] === "string" ? params[key] : "");
  const query = text("q");
  const status = text("status");
  const offset = Math.max(0, Number(text("offset")) || 0);

  const search = new URLSearchParams();
  if (query) search.set("q", query);
  if (status) search.set("status", status);
  if (offset > 0) search.set("offset", String(offset));
  search.set("limit", String(ACCOUNTS_PAGE));

  // Both reads must succeed: accounts without role names would show bare codes.
  const policy = { here: accountsHref({ query, status, offset }) };
  const [{ items, total }, { items: roles }] = await Promise.all([
    readPage(`/users?${search}`, (payload) => accountListSchema.parse(payload), policy),
    readPage("/roles", (payload) => roleListSchema.parse(payload), policy),
  ]);

  const pageHref = (next: number) => accountsHref({ query, status, offset: next });
  const mail = await mailRead;

  return (
    <Band fill className="flex flex-col gap-8 py-12">
      <AccountFilters query={query} status={status} dict={dict} />

      {/* The selection lives in layout.tsx so it survives searches. `pageIds`
         lets the bar say how much of it is off this page. */}
      <SelectionBar
        dict={dict}
        roles={roles}
        pageIds={items.map((account) => account.id)}
        locale={locale}
        mail={mail}
      />

      <AccountRegister
        accounts={items}
        total={total}
        offset={offset}
        pageHref={pageHref}
        filtered={Boolean(query || status)}
        roles={roles}
        dict={dict}
        locale={locale}
        mail={mail}
      />
    </Band>
  );
}
