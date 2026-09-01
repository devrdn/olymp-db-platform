import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { serverRequest } from "@/lib/api/server";
import { settingsSchema } from "@/lib/api/settings";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary } from "@/lib/i18n/server";

import { SettingsForm } from "./settings-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.settings.heading };
}

/**
 * The installation's own settings.
 *
 * `/settings/all` rather than the public read: this screen edits everything,
 * and the public endpoint deliberately answers with an allow-list. Two
 * endpoints for what looks like one resource is the point — the open one can
 * only ever say what somebody declared safe to say.
 */
export default async function SettingsPage() {
  const dict = await activeDictionary();

  const payload = await serverRequest("/settings/all").catch((error: unknown) => {
    const target = authRecoveryRedirect(error, "/settings");
    if (target) redirect(target);
    throw error;
  });

  const settings = settingsSchema.parse(payload);

  return (
    <Band fill className="flex flex-col gap-8 py-12">
      <div className="flex flex-col gap-3">
        <h1 className="text-h2 text-ink">{dict.settings.heading}</h1>
        <p className="max-w-lede text-body text-ink-2">{dict.settings.lede}</p>
      </div>

      <SettingsForm settings={settings} dict={dict} />
    </Band>
  );
}
