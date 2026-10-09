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
 * The audit trail. A Server Component; filters and paging live in the address,
 * so a view is a shareable link. The filter's action list comes from `GET
 * /audit/actions`, so an action can be filtered before any entry for it is on
 * screen.
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

  // proxy.ts only saw a cookie; this answer says whether the session is alive.
  // Both reads run together.
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
  // Sorted for the dropdown; the server groups by domain.
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
