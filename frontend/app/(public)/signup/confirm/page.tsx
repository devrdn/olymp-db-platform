import { Band } from "@/components/layout/band";
import { signupEmail } from "@/lib/auth/signup-email";
import { activeDictionary } from "@/lib/i18n/server";

import { ConfirmForm, StartOver } from "./confirm-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.auth.confirm.title, robots: { index: false, follow: false } };
}

/**
 * Where a sign-up waits for its mailed code. The address comes from the
 * cookie the sign-up form left, never from the URL; the token binding the
 * sign-up to this browser stays in the API's own cookie.
 */
export default async function ConfirmPage() {
  const [email, dict] = await Promise.all([signupEmail(), activeDictionary()]);
  const t = dict.auth.confirm;

  return (
    <Band fill className="py-0">
      <div className="grid flex-1 content-center gap-12 xl:grid-cols-[minmax(0,1fr)_1px_24rem] xl:content-stretch xl:gap-x-10">
        <div className="flex flex-col justify-center gap-8 xl:py-24">
          <h1 className="max-w-head text-display text-balance text-ink">{t.title}</h1>
          {email ? <p className="max-w-lede text-lede break-words text-ink-2">{t.lede.replace("{email}", email)}</p> : null}
        </div>

        <div aria-hidden className="hidden bg-line xl:block" />

        <div className="flex flex-col justify-center xl:py-24">
          {email ? (
            <ConfirmForm t={t} errors={dict.errors} />
          ) : (
            <div className="flex max-w-96 flex-col gap-4">
              <p className="text-body text-ink-2">{t.nothingToConfirm}</p>
              <StartOver t={t} />
            </div>
          )}
        </div>
      </div>
    </Band>
  );
}
