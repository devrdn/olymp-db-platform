import { ArrowLeft } from "lucide-react";
import Link from "next/link";

import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings-terms";
import { LOCALES, type Locale } from "@/lib/i18n/config";
import { getDictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest } from "../../contest";
import { fetchResultsAction } from "./actions";
import { ExportForm } from "./export-form";
import type { ResultsPdfTexts } from "./pdf/results-document";

/**
 * The results export, its own page rather than a dialog so a long list
 * scrolls as a page does on a phone. A refused preview (no permission, too
 * many registrations) is said in place of the form.
 */
export default async function ResultsExportPage(props: PageProps<"/contests/[contestId]/standings/export">) {
  const [{ contestId }, locale, dict] = await Promise.all([props.params, activeLocale(), activeDictionary()]);

  const [contest, preview, brand, pdfTexts] = await Promise.all([
    loadContest(contestId),
    fetchResultsAction(contestId),
    branding(),
    allPdfTexts(),
  ]);
  const t = dict.leaderboard.export;

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-3">
        <Link
          href={`/contests/${contest.id}/standings`}
          className="inline-flex items-center gap-1.5 self-start text-small text-ink-2 hover:text-ink"
        >
          <ArrowLeft className="size-4" strokeWidth={1.75} aria-hidden />
          {t.back}
        </Link>
        <h2 className="text-h3 text-ink">{t.heading}</h2>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>
      </div>
      {preview.kind === "ok" ? (
        <ExportForm
          contestId={contest.id}
          report={preview.report}
          dict={dict}
          locale={locale}
          pdfTexts={pdfTexts}
          installationName={brand.name}
          logoHref={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined}
        />
      ) : (
        <p role="alert" className="max-w-body bg-bad-wash px-3 py-2 text-small text-bad">
          {messageForCode(preview.code, dict.errors)}
        </p>
      )}
    </div>
  );
}

async function allPdfTexts(): Promise<Record<Locale, ResultsPdfTexts>> {
  const dictionaries = await Promise.all(LOCALES.map((code) => getDictionary(code)));
  return Object.fromEntries(LOCALES.map((code, i) => [code, dictionaries[i].leaderboard.export.pdf])) as Record<
    Locale,
    ResultsPdfTexts
  >;
}
