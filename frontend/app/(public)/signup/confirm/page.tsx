import { Band } from "@/components/layout/band";
import { showableEmail } from "@/lib/auth/signup-rules";
import { activeDictionary } from "@/lib/i18n/server";

import { ConfirmForm } from "./confirm-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.auth.confirm.title, robots: { index: false, follow: false } };
}

/**
 * Where a sign-up waits for its mailed code. The address arrives in the
 * query from the sign-up form; the token binding the sign-up to this browser
 * stays in its cookie.
 */
export default async function ConfirmPage(props: PageProps<"/signup/confirm">) {
  const [params, dict] = await Promise.all([props.searchParams, activeDictionary()]);
  const t = dict.auth.confirm;

  const typed = typeof params.email === "string" ? params.email.trim() : "";
  const email = showableEmail(typed);

  return (
    <Band fill className="py-0">
      <div className="grid flex-1 content-center gap-12 xl:grid-cols-[minmax(0,1fr)_1px_24rem] xl:content-stretch xl:gap-x-10">
        <div className="flex flex-col justify-center gap-8 xl:py-24">
          <h1 className="max-w-head text-display text-balance text-ink">{t.title}</h1>
          <p className="max-w-lede text-lede break-words text-ink-2">
            {email ? t.lede.replace("{email}", email) : t.ledeNoEmail}
          </p>
        </div>

        <div aria-hidden className="hidden bg-line xl:block" />

        <div className="flex flex-col justify-center xl:py-24">
          <ConfirmForm t={t} errors={dict.errors} email={email} />
        </div>
      </div>
    </Band>
  );
}
