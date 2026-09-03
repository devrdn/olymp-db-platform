import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { auditActionsSchema, auditPageSchema, auditSearch, dayBounds } from "@/lib/api/audit";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { AuditFilters } from "./filters";
import { AuditTrailRegister } from "./trail";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.audit.heading };
}

/**
 * The audit trail.
 *
 * A Server Component with the session already in hand, so no trail data and no
 * token reach the browser beyond what is rendered. Filters and the page live
 * in the address, which makes a view shareable — "here is every refusal from
 * that address on Tuesday" is a link.
 *
 * The action list offered by the filter is fetched beside the trail
 * (`GET /audit/actions`) rather than built from the rows in hand — the
 * accounts screen fetches its role catalogue beside `/users` for the same
 * reason. A list built from the current page can only ever offer an action
 * already on screen, which made a deletion unfilterable until one happened to
 * appear by accident; the endpoint offers the whole vocabulary before a
 * single matching entry exists.
 */
export default async function AuditPage(props: PageProps<"/audit">) {
  const [params, locale, dict] = await Promise.all([
    props.searchParams,
    activeLocale(),
    activeDictionary(),
  ]);

  const text = (key: string) => (typeof params[key] === "string" ? params[key] : "");
  const action = text("action");
  const entity = text("entity");
  const from = text("from");
  const to = text("to");
  const offset = Math.max(0, Number(text("offset")) || 0);

  const search = auditSearch({ action, entity, offset, ...dayBounds(from, to) });

  const here = () => {
    const shown = new URLSearchParams();
    for (const [key, value] of Object.entries({ action, entity, from, to })) {
      if (value) shown.set(key, value);
    }
    return shown.size > 0 ? `/audit?${shown}` : "/audit";
  };

  // The proxy could only see that a session cookie exists; whether it is
  // still worth anything is this answer.
  //
  // Both requests together: they render one screen, and a page whose filter
  // could only name the actions on it would be no better than the bug this
  // replaces.
  const onAuthFailure = (error: unknown) => {
    const target = authRecoveryRedirect(error, here());
    if (target) redirect(target);
    throw error;
  };
  const [payload, actionsPayload] = await Promise.all([
    serverRequest(`/audit?${search}`).catch(onAuthFailure),
    serverRequest(`/audit/actions`).catch(onAuthFailure),
  ]);

  const { items, total } = auditPageSchema.parse(payload);
  // Sorted for the dropdown; the server's own order is grouped by domain
  // (auth, then accounts, then contests…), which reads well in source but not
  // as a pick list.
  const { items: actions } = auditActionsSchema.parse(actionsPayload);
  actions.sort();

  const pageHref = (next: number) => {
    const shown = new URLSearchParams();
    for (const [key, value] of Object.entries({ action, entity, from, to })) {
      if (value) shown.set(key, value);
    }
    if (next > 0) shown.set("offset", String(next));
    return shown.size > 0 ? `/audit?${shown}` : "/audit";
  };

  return (
    <Band fill className="flex flex-col gap-8 py-12">
      <div className="flex flex-col gap-2">
        <h1 className="text-h2 text-ink">{dict.audit.heading}</h1>
        <p className="max-w-prose text-body text-ink-2">{dict.audit.lede}</p>
      </div>

      <AuditFilters
        action={action}
        entity={entity}
        from={from}
        to={to}
        actions={actions}
        dict={dict}
      />

      <AuditTrailRegister
        entries={items}
        total={total}
        offset={offset}
        pageHref={pageHref}
        filtered={Boolean(action || entity || from || to)}
        dict={dict}
        locale={locale}
      />
    </Band>
  );
}
