import { Band } from "@/components/layout/band";
import { InlineLink } from "@/components/ui/inline-link";
import { fetchAuthMethods } from "@/lib/api/auth-methods";
import { activeDictionary } from "@/lib/i18n/server";

import { SignUpForm } from "./signup-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.auth.signUp.title };
}

/**
 * Self-service sign-up, in the sign-in screen's composition: both are part of
 * being let in. Not the event registration form at `/register`, which makes no
 * password and needs no mail.
 */
export default async function SignUpPage() {
  const [dict, methods] = await Promise.all([activeDictionary(), fetchAuthMethods()]);
  const t = dict.auth.signUp;

  // Only a definite "no" hides the form; when the API cannot say, it decides
  // when the form is sent.
  const offered = methods?.passwordSignup ?? true;

  return (
    <Band fill className="py-0">
      <div className="grid flex-1 content-center gap-12 xl:grid-cols-[minmax(0,1fr)_1px_24rem] xl:content-stretch xl:gap-x-10">
        <div className="flex flex-col justify-center gap-8 xl:py-24">
          <h1 className="max-w-head text-display text-balance text-ink">{t.title}</h1>
          <p className="max-w-lede text-lede text-ink-2">{t.lede}</p>
        </div>

        <div aria-hidden className="hidden bg-line xl:block" />

        <div className="flex flex-col justify-center gap-6 xl:py-24">
          {offered ? (
            <SignUpForm t={t} errors={dict.errors} />
          ) : (
            <div className="flex max-w-96 flex-col gap-4">
              <p className="text-body text-ink-2">{dict.errors.signup_not_available}</p>
              <InlineLink href="/login" className="self-start">
                {t.signIn}
              </InlineLink>
            </div>
          )}
        </div>
      </div>
    </Band>
  );
}
