import { Band } from "@/components/layout/band";
import { auditActionsSchema, auditPageSchema, auditSearch, dayBounds } from "@/lib/api/audit";
import { readPage } from "@/lib/api/page-read";
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

  const pageHref = (next: number) => {
    const shown = new URLSearchParams();
    for (const [key, value] of Object.entries({ action, entity, from, to })) {
      if (value) shown.set(key, value);
    }
    if (next > 0) shown.set("offset", String(next));
    return shown.size > 0 ? `/audit?${shown}` : "/audit";
  };

  // Both reads run together; sign-in leads back to this view, offset dropped.
  const policy = { here: pageHref(0) };
  const [{ items, total }, { items: actions }] = await Promise.all([
    readPage(`/audit?${search}`, (payload) => auditPageSchema.parse(payload), policy),
    readPage("/audit/actions", (payload) => auditActionsSchema.parse(payload), policy),
  ]);
  // Sorted for the dropdown; the server groups by domain.
  actions.sort();

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
