import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { serverRequest } from "@/lib/api/server";
import { settingsSchema } from "@/lib/api/settings";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary } from "@/lib/i18n/server";

import { ImageSlots } from "./image-slots";
import { SettingsForm } from "./settings-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.settings.heading };
}

/**
 * The installation's settings, read from `/settings/all`: the public endpoint
 * answers only an allow-list of what is safe to say.
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

      <ImageSlots images={settings.images} dict={dict} />
    </Band>
  );
}
