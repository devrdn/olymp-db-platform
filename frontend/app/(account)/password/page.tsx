import { Band } from "@/components/layout/band";
import { activeDictionary } from "@/lib/i18n/server";

import { ChangePasswordForm } from "./change-password-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.auth.changePassword.title };
}

/**
 * Replaces the issued password. Reached from sign-in when the login says so,
 * and from any screen after the API answers `password_change_required` (all but
 * three endpoints do). Shares the sign-in screen's composition: both are part
 * of being let in.
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
          {/* Every session ends here; say so before the change. */}
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
