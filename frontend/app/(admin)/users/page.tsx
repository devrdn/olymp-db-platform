import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { accountListSchema, roleListSchema } from "@/lib/api/accounts";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { AccountRegister, ACCOUNTS_PAGE } from "./account-register";
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

  const text = (key: string) => (typeof params[key] === "string" ? params[key] : "");
  const query = text("q");
  const status = text("status");
  const offset = Math.max(0, Number(text("offset")) || 0);

  const search = new URLSearchParams();
  if (query) search.set("q", query);
  if (status) search.set("status", status);
  if (offset > 0) search.set("offset", String(offset));
  search.set("limit", String(ACCOUNTS_PAGE));

  // proxy.ts only saw a cookie; this answer says whether the session is alive,
  // and a dead one goes back to sign-in. Both reads must succeed: accounts
  // without role names would show bare codes.
  const [accountsPayload, rolesPayload] = await Promise.all([
    serverRequest(`/users?${search}`),
    serverRequest("/roles"),
  ]).catch((error: unknown) => {
    const target = authRecoveryRedirect(error, accountsHref({ query, status, offset }));
    if (target) redirect(target);
    throw error;
  });

  const { items, total } = accountListSchema.parse(accountsPayload);
  const { items: roles } = roleListSchema.parse(rolesPayload);

  const pageHref = (next: number) => accountsHref({ query, status, offset: next });

  return (
    <Band fill className="flex flex-col gap-8 py-12">
      <AccountFilters query={query} status={status} dict={dict} />

      {/* The selection lives in layout.tsx so it survives searches. `pageIds`
         lets the bar say how much of it is off this page. */}
      <SelectionBar dict={dict} roles={roles} pageIds={items.map((account) => account.id)} />

      <AccountRegister
        accounts={items}
        total={total}
        offset={offset}
        pageHref={pageHref}
        filtered={Boolean(query || status)}
        roles={roles}
        dict={dict}
        locale={locale}
      />
    </Band>
  );
}
