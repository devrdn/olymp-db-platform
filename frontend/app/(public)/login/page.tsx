import { Band } from "@/components/layout/band";
import { activeDictionary } from "@/lib/i18n/server";

import { SignInForm } from "./sign-in-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.auth.signIn.title };
}

/**
 * Sign-in for everyone; where an account lands depends on its permissions. The
 * composition rests on scale contrast (a 92px word against a 14px pill), with
 * columns divided by a hairline, not a background change.
 */
export default async function LoginPage(props: PageProps<"/login">) {
  const [params, dict] = await Promise.all([
    props.searchParams,
    activeDictionary(),
  ]);
  const next = typeof params.next === "string" ? params.next : undefined;

  /**
   * The password change retires every session, so the only carrier is the
   * address. A flag, not a message: the address may only select a dictionary
   * sentence, never supply text.
   */
  const passwordChanged = params.changed === "1";

  const t = dict.auth.signIn;

  return (
    <Band fill className="py-0">
      {/* Two columns only from 1280px; below that the split leaves two narrow
         strips. The rule runs the band's full height. One horizontal module
         sets the band padding and both distances to the rule, which also aligns
         the form with the app bar. */}
      <div className="grid flex-1 content-center gap-12 xl:grid-cols-[minmax(0,1fr)_1px_24rem] xl:content-stretch xl:gap-x-10">
        <div className="flex flex-col justify-center gap-8 xl:py-24">
          <h1 className="max-w-head text-display text-balance text-ink">
            {t.title}
          </h1>
          <p className="max-w-lede text-lede text-ink-2">{t.lede}</p>
          {/* The interface face: the narrative face is reserved for the story. */}
          <p className="max-w-body text-body text-ink-3">{t.aside}</p>
        </div>

        <div aria-hidden className="hidden bg-line xl:block" />

        <div className="flex flex-col justify-center gap-6 xl:py-24">
          {/* `status`, not `alert`: good news should not interrupt. Above the
             form, since it explains why the form is shown again. */}
          {passwordChanged ? (
            <p role="status" className="max-w-96 border-l-2 border-good pl-3 text-small text-ink-2">
              {t.passwordChanged}
            </p>
          ) : null}

          <SignInForm dict={dict} next={next} />
        </div>
      </div>
    </Band>
  );
}
