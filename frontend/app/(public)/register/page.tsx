import type { Metadata } from "next";

import { Band } from "@/components/layout/band";
import { activeDictionary } from "@/lib/i18n/server";

import { RegisterForm } from "./register-form";

/** A page for one evening's guests, not for search results. */
export async function generateMetadata(): Promise<Metadata> {
  const dict = await activeDictionary();
  return { title: dict.registration.title, robots: { index: false, follow: false } };
}

/** The event registration form, open to visitors without an account. */
export default async function RegisterPage() {
  const dict = await activeDictionary();
  const t = dict.registration;

  return (
    <Band fill className="gap-10 py-16">
      <div className="flex flex-col gap-4">
        <h1 className="max-w-head text-h2 text-balance text-ink">{t.title}</h1>
        <p className="max-w-lede text-lede text-ink-2">{t.lede}</p>
      </div>

      <RegisterForm t={t} errors={dict.errors} />
    </Band>
  );
}
