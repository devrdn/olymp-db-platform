import { Band } from "@/components/layout/band";
import { PublicShell } from "@/components/layout/public-shell";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

import { SignInForm } from "./sign-in-form";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.auth.signIn.title };
}

/**
 * Sign-in, for participants and staff alike: the API has no separate admin
 * endpoint, and where an account lands afterwards is decided by the
 * permissions it turns out to hold.
 *
 * The composition is the direction's own, not the split screen the pattern
 * usually gets. One word at the display step — 92px at weight 400 — against a
 * form whose submit is a 14px pill: that scale contrast is what carries the
 * page, and it is the thing colour is normally asked to do instead (spec
 * section 5). The two columns are separated by a hairline rather than by a
 * change of background, because this system draws with rules, not with panels.
 */
export default async function LoginPage(props: PageProps<"/login">) {
  const [params, dict, locale, theme] = await Promise.all([
    props.searchParams,
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);
  const next = typeof params.next === "string" ? params.next : undefined;
  const t = dict.auth.signIn;

  return (
    <PublicShell locale={locale} theme={theme} dict={dict}>
      <Band fill className="py-0">
        {/* Two columns only from 1280px up, not from the layout breakpoint.
            Between the two the split still technically fits, and it reads
            badly: the heading column and the form column come out the same
            width, the lede breaks after three words, and the page looks like
            two narrow strips rather than a composition. Below that the form
            goes under the heading, where it has the whole column.

            The rule runs the full height of the band rather than the height of
            the text beside it. A division that stops where the content stops
            reads as a gap; one that runs edge to edge is structure, which is
            what this system draws with instead of panels.

            One horizontal module, and every vertical in the composition sits on
            it: the band's own padding, the heading's distance to the rule and
            the rule's distance to the form are the same figure. With a wider
            column gap the form stood 80px from the rule and 40px from the
            hatched field, so the panel read as shoved against the right edge —
            an asymmetry invisible in a mock-up and obvious on a wide screen.
            Matching the band's padding also lines the form up with the app bar,
            which is measured from that same edge. */}
        <div className="grid flex-1 content-center gap-12 xl:grid-cols-[minmax(0,1fr)_1px_24rem] xl:content-stretch xl:gap-x-10">
          <div className="flex flex-col justify-center gap-8 xl:py-24">
            <h1 className="max-w-head text-display text-balance text-ink">{t.title}</h1>
            <p className="max-w-lede text-lede text-ink-2">{t.lede}</p>
            {/* Set in the interface face, not in Literata: the narrative
                register belongs to the crime story and to nothing else
               . */}
            <p className="max-w-body text-body text-ink-3">{t.aside}</p>
          </div>

          <div aria-hidden className="hidden bg-line xl:block" />

          <div className="flex flex-col justify-center xl:py-24">
            <SignInForm dict={dict} next={next} />
          </div>
        </div>
      </Band>
    </PublicShell>
  );
}
