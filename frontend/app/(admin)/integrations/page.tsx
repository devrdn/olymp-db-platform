import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { CopyButton } from "@/components/product/sql-block";
import { ProviderLogo } from "@/components/product/provider-logo";
import { integrationsSchema } from "@/lib/api/integrations";
import { serverRequest } from "@/lib/api/server";
import { PROVIDER_NAMES } from "@/lib/auth/external";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary } from "@/lib/i18n/server";

import { ProviderSwitch } from "./provider-switch";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.settings.integrations.heading };
}

/** Every sign-in provider this release knows, configured or not, and the switch of each configured one. */
export default async function IntegrationsPage() {
  const dict = await activeDictionary();
  const t = dict.settings.integrations;

  const payload = await serverRequest("/integrations").catch((error: unknown) => {
    const target = authRecoveryRedirect(error, "/integrations");
    if (target) redirect(target);
    throw error;
  });
  const providers = integrationsSchema.parse(payload);

  return (
    <Band fill className="flex flex-col gap-8 py-12">
      <div className="flex flex-col gap-3">
        <h1 className="text-h2 text-ink">{t.heading}</h1>
        <p className="max-w-lede text-body text-ink-2">{t.lede}</p>
      </div>

      {providers.map((provider) => {
        const headingId = `integration-${provider.id}`;
        return (
          <section
            key={provider.id}
            aria-labelledby={headingId}
            className="flex max-w-narrative flex-col gap-4 border-t border-line pt-6"
          >
            <div className="flex flex-wrap items-center gap-3">
              <ProviderLogo provider={provider.id} />
              <h2 id={headingId} className="text-h3 text-ink">
                {PROVIDER_NAMES[provider.id]}
              </h2>
              <span className="text-small text-ink-3">
                {!provider.configured ? t.notConfigured : provider.enabled ? t.on : t.off}
              </span>
            </div>

            {!provider.configured ? (
              <div className="flex flex-col gap-1.5">
                <p className="max-w-body text-body text-ink-2">{t.toConfigure}</p>
                <ul className="flex flex-col gap-1">
                  {provider.variables.map((name) => (
                    <li key={name}>
                      <code className="font-mono text-data text-ink">{name}</code>
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}

            <div className="flex flex-col gap-1.5">
              <p className="text-small text-ink-3">{t.redirectUri}</p>
              {provider.redirectUri ? (
                <div className="flex flex-wrap items-center gap-3">
                  <code className="min-w-0 break-all font-mono text-data text-ink">{provider.redirectUri}</code>
                  <CopyButton
                    text={provider.redirectUri}
                    labels={{ copy: t.copy, copied: t.copied, copyFailed: t.copyFailed }}
                  />
                </div>
              ) : (
                <p className="max-w-body text-body text-ink-2">{t.noPublicUrl}</p>
              )}
            </div>

            {provider.configured ? (
              <div className="flex flex-col gap-3">
                <ProviderSwitch provider={provider.id} enabled={provider.enabled} dict={dict} />
                <p className="max-w-body text-small text-ink-3">{t.note}</p>
              </div>
            ) : null}
          </section>
        );
      })}
    </Band>
  );
}
