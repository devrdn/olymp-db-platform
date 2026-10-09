import Link from "next/link";

import { Band } from "@/components/layout/band";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { NewContestForm } from "./new-contest-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.workspace.create.heading };
}

/** A new contest. `new` cannot collide with a contest id, which is a UUID checked before use. */
export default async function NewContestPage() {
  const [dict, locale] = await Promise.all([activeDictionary(), activeLocale()]);
  const t = dict.workspace.create;

  return (
    <Band fill className="gap-8 py-12">
      <div className="flex flex-col gap-4">
        <Link
          href="/contests"
          className="w-fit font-mono text-data text-ink-3 transition-colors duration-(--t-input) ease-standard hover:text-ink"
        >
          {dict.workspace.backToRegister}
        </Link>
        <h1 className="max-w-head text-h2 text-balance text-ink">{t.heading}</h1>
        <p className="max-w-lede text-lede text-ink-2">{t.lede}</p>
      </div>

      <NewContestForm dict={dict} locale={locale} />
    </Band>
  );
}
