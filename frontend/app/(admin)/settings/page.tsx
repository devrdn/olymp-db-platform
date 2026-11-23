import { Band } from "@/components/layout/band";
import { readPage } from "@/lib/api/page-read";
import { mailStatusSchema, settingsSchema } from "@/lib/api/settings";
import { activeDictionary } from "@/lib/i18n/server";

import { ArchiveSwitch } from "./archive-switch";
import { ImageSlots } from "./image-slots";
import { MailTest } from "./mail-test";
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

  const { settings, mail } = await readPage(
    "/settings/all",
    (payload) => ({ settings: settingsSchema.parse(payload), mail: mailStatusSchema.parse(payload) }),
    { here: "/settings" },
  );

  return (
    <Band fill className="flex flex-col gap-8 py-12">
      <div className="flex flex-col gap-3">
        <h1 className="text-h2 text-ink">{dict.settings.heading}</h1>
        <p className="max-w-lede text-body text-ink-2">{dict.settings.lede}</p>
      </div>

      <SettingsForm settings={settings} dict={dict} />

      <ArchiveSwitch enabled={settings.archive} dict={dict} />

      <MailTest mail={mail} dict={dict} />

      <ImageSlots images={settings.images} dict={dict} />
    </Band>
  );
}
