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
 * Account management, the last screen step 2 owed.
 *
 * Until now accounts could be created and blocked only through the API, which
 * meant a person with the right to manage them still needed a terminal and a
 * session token to use it. Creating one test student was a hand-written
 * program.
 *
 * A Server Component: the data is fetched where the session already is, so no
 * token reaches the browser and the first paint carries the rows. Filters live
 * in the URL, which makes the view shareable and the reset a plain link.
 *
 * The role catalogue is fetched beside the page rather than baked in. Roles
 * are rows in a table precisely so that adding one is data, and a list
 * repeated in the interface would be a second copy that nothing keeps in step.
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

  // The proxy could only see that a session cookie exists; whether it is still
  // worth anything is this answer. A dead session goes back to the form, which
  // the error boundary could not offer.
  //
  // Both requests together: they render one screen, and a page that showed
  // accounts with no role names would be showing codes for no reason.
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

      {/* The selection itself lives one level up, in layout.tsx — it has to
          survive this component re-rendering on every search, which a
          provider mounted here could not. `pageIds` tells the bar which of
          the whole selection are actually in `items` below, so it can say
          when part of a pick spans other pages or an earlier search. */}
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
