import { LanguageSwitcher } from "@/components/layout/language-switcher";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { SignInForm } from "./sign-in-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.auth.signIn.title };
}

/**
 * Sign-in, for participants and staff alike: the API has no separate admin
 * endpoint, and where an account lands afterwards is decided by the
 * permissions it turns out to hold.
 */
export default async function LoginPage() {
  const [dict, locale] = await Promise.all([activeDictionary(), activeLocale()]);
  const t = dict.auth.signIn;

  return (
    <main className="grid min-h-[100dvh] lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
      <div className="flex items-center justify-center px-6 py-16 sm:px-10">
        <div className="flex w-full max-w-sm flex-col gap-8">
          <div className="flex items-center justify-between gap-4">
            <span className="bg-cta px-2.5 py-1 font-mono text-[0.6875rem] font-bold tracking-[0.14em] text-cta-fg">
              DB CONTEST
            </span>
            <LanguageSwitcher current={locale} />
          </div>

          <div className="flex flex-col gap-2">
            <h1 className="text-3xl font-normal tracking-[-0.03em]">{t.title}</h1>
            <p className="text-sm text-ink-2">{t.lede}</p>
          </div>

          <SignInForm dict={dict} />
        </div>
      </div>

      {/* The narrative register lives here and nowhere else in the product. */}
      <aside className="hidden flex-col justify-center gap-4 border-l border-line-2 bg-sunk px-10 py-16 lg:flex">
        <p className="font-serif text-lg leading-relaxed text-ink-2 max-w-[38ch]">
          {t.aside}
        </p>
      </aside>
    </main>
  );
}
