import { Band } from "@/components/layout/band";
import { fetchAuthMethods } from "@/lib/api/auth-methods";
import { activeDictionary } from "@/lib/i18n/server";

import { ForgotForm, Unavailable } from "./forgot-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.auth.forgot.title };
}

/** A request for a reset link, in the sign-in screen's composition. */
export default async function ForgotPage() {
  const [dict, methods] = await Promise.all([activeDictionary(), fetchAuthMethods()]);
  const t = dict.auth.forgot;

  // Only a definite "no" hides the form; when the API cannot say, it decides
  // when the form is sent.
  const offered = methods?.passwordReset ?? true;

  return (
    <Band fill className="py-0">
      <div className="grid flex-1 content-center gap-12 xl:grid-cols-[minmax(0,1fr)_1px_24rem] xl:content-stretch xl:gap-x-10">
        <div className="flex flex-col justify-center gap-8 xl:py-24">
          <h1 className="max-w-head text-display text-balance text-ink">{t.title}</h1>
          <p className="max-w-lede text-lede text-ink-2">{t.lede}</p>
        </div>

        <div aria-hidden className="hidden bg-line xl:block" />

        <div className="flex flex-col justify-center xl:py-24">
          {offered ? <ForgotForm t={t} errors={dict.errors} /> : <Unavailable t={t} />}
        </div>
      </div>
    </Band>
  );
}
