import { Band } from "@/components/layout/band";
import { activeDictionary } from "@/lib/i18n/server";

import { ChangePasswordForm } from "./change-password-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.auth.changePassword.title };
}

/**
 * Replacing the password an administrator handed over.
 *
 * Reached two ways, and both matter. Sign-in sends an account here directly
 * when the login response says so; and every other screen sends it here after
 * the API refuses with `password_change_required`, which it does on all but
 * three endpoints. The second path is what makes the screen real — without it,
 * an account that navigates anywhere by hand meets an error page offering a
 * retry that can never work.
 *
 * The composition is the sign-in screen's, deliberately: the same display
 * heading against the same hairline against the same 384px form column. These
 * two are one moment in the product — being let in — and a second layout for
 * the second half would read as a different application.
 */
export default async function PasswordPage() {
  const dict = await activeDictionary();
  const t = dict.auth.changePassword;

  return (
    <Band fill className="py-0">
      <div className="grid flex-1 content-center gap-12 xl:grid-cols-[minmax(0,1fr)_1px_24rem] xl:content-stretch xl:gap-x-10">
        <div className="flex flex-col justify-center gap-8 xl:py-24">
          <h1 className="max-w-head text-display text-balance text-ink">{t.title}</h1>
          <p className="max-w-lede text-lede text-ink-2">{t.lede}</p>
          {/* Stated before the change, not discovered after it: every session
              ends here, so an author with a contest open in another tab is
              told it is about to be signed out rather than finding out by
              being signed out. */}
          <p className="max-w-body text-body text-ink-3">{t.note}</p>
        </div>

        <div aria-hidden className="hidden bg-line xl:block" />

        <div className="flex flex-col justify-center xl:py-24">
          <ChangePasswordForm dict={dict} />
        </div>
      </div>
    </Band>
  );
}
