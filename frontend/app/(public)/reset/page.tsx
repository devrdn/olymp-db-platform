import type { Metadata } from "next";

import { Band } from "@/components/layout/band";
import { InlineLink } from "@/components/ui/inline-link";
import { isResetToken } from "@/lib/api/recovery";
import { activeDictionary } from "@/lib/i18n/server";

import { ResetForm } from "./reset-form";

/**
 * The address carries the link's token. next.config.ts sends the same
 * referrer policy as a header; the document states it too, so it holds even
 * where a proxy in front rewrites that header.
 */
export async function generateMetadata(): Promise<Metadata> {
  const dict = await activeDictionary();
  return { title: dict.auth.reset.title, referrer: "no-referrer", robots: { index: false, follow: false } };
}

/** Where a mailed reset link opens (`recovery.ResetPath`). */
export default async function ResetPage(props: PageProps<"/reset">) {
  const [params, dict] = await Promise.all([props.searchParams, activeDictionary()]);
  const t = dict.auth.reset;

  const token = typeof params.token === "string" ? params.token : "";

  return (
    <Band fill className="py-0">
      <div className="grid flex-1 content-center gap-12 xl:grid-cols-[minmax(0,1fr)_1px_24rem] xl:content-stretch xl:gap-x-10">
        <div className="flex flex-col justify-center gap-8 xl:py-24">
          <h1 className="max-w-head text-display text-balance text-ink">{t.title}</h1>
          <p className="max-w-lede text-lede text-ink-2">{t.lede}</p>
        </div>

        <div aria-hidden className="hidden bg-line xl:block" />

        <div className="flex flex-col justify-center xl:py-24">
          {/* A token no link could carry is answered as an unusable link, without asking the API. */}
          {isResetToken(token) ? (
            <ResetForm t={t} errors={dict.errors} token={token} />
          ) : (
            <div className="flex max-w-96 flex-col gap-4">
              <p className="text-body text-ink-2">{dict.errors.reset_link_invalid}</p>
              <InlineLink href="/forgot" className="self-start">
                {t.requestNew}
              </InlineLink>
            </div>
          )}
        </div>
      </div>
    </Band>
  );
}
