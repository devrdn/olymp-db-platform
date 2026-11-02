"use client";

import { FileDown, RefreshCw } from "lucide-react";
import { useId, useState } from "react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { RESULTS_DETAILS, RESULTS_FIELDS, type ResultsDetail, type ResultsField, type ResultsReport } from "@/lib/api/results";
import { saveBlob } from "@/lib/download";
import { formatTime } from "@/lib/format/datetime";
import { LOCALE_NAMES, LOCALES, type Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";
import { cn } from "@/lib/utils";

import { fetchResultsAction } from "./actions";
import { ParticipantList } from "./participant-list";
import type { ResultsPdfTexts } from "./pdf/results-document";
import { exportBody, protocolAvailable, resultsFileName, type ExportKind } from "./request";
import { downloadResultsCsv, fetchResultsReport } from "./requests";
import { initialSelection, keptOnRefresh, selectedInOrder } from "./selection";

const KINDS: readonly ExportKind[] = ["csv", "table", "protocol"];

/** The contest's own language when the PDF speaks it, else the interface's. */
function pdfLanguage(contestLanguage: string, locale: Locale): Locale {
  return (LOCALES as readonly string[]).includes(contestLanguage) ? (contestLanguage as Locale) : locale;
}

type Failure = { where: "refresh" | "download"; message: string };

/**
 * The results export: whom on one side, what and how on the other, and the
 * download. The list is the snapshot the page loaded, refreshed only on
 * request, so a row never moves under a finger about to tick it. The PDF
 * maker is imported on the click that needs it.
 */
export function ExportForm({
  contestId,
  report: loaded,
  dict,
  locale,
  pdfTexts,
  installationName,
  logoHref,
}: {
  contestId: string;
  report: ResultsReport;
  dict: Dictionary;
  locale: Locale;
  /** Every PDF language's strings: the PDF is printed in the one chosen here, not the interface's. */
  pdfTexts: Record<Locale, ResultsPdfTexts>;
  installationName: string;
  /** The installation's uploaded logo; none means the product mark. */
  logoHref?: string;
}) {
  const t = dict.leaderboard.export;
  const ids = useId();
  const [report, setReport] = useState(loaded);
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => initialSelection(loaded.rows));
  const [pickedKind, setKind] = useState<ExportKind>("csv");
  const [detail, setDetail] = useState<ResultsDetail>("summary");
  const [fields, setFields] = useState<ReadonlySet<ResultsField>>(() => new Set(["full_name", "login"]));
  const [excel, setExcel] = useState(false);
  const [language, setLanguage] = useState<Locale>(() => pdfLanguage(loaded.contest.defaultLanguage, locale));
  const [chair, setChair] = useState("");
  const [members, setMembers] = useState("");
  const [busy, setBusy] = useState<"refresh" | "download" | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);

  const offered = RESULTS_FIELDS.filter((field) => field !== "email" || report.mayExportEmail);
  const chosen = new Set(offered.filter((field) => fields.has(field)));
  const count = selectedInOrder(selected, report.rows).length;
  const protocolOffered = protocolAvailable(report.status);
  // A refresh can make the report preliminary again under a picked protocol.
  const kind: ExportKind = pickedKind === "protocol" && !protocolOffered ? "csv" : pickedKind;
  const pdf = kind !== "csv";

  async function refresh() {
    setBusy("refresh");
    setFailure(null);
    try {
      const result = await fetchResultsAction(contestId);
      if (result.kind === "ok") {
        setReport(result.report);
        setSelected((previous) => keptOnRefresh(previous, result.report.rows));
      } else {
        setFailure({ where: "refresh", message: messageForCode(result.code, dict.errors) });
      }
    } catch {
      setFailure({ where: "refresh", message: messageForCode("unreachable", dict.errors) });
    } finally {
      setBusy(null);
    }
  }

  async function download() {
    const registrations = selectedInOrder(selected, report.rows);
    if (registrations.length === 0) return;
    setBusy("download");
    setFailure(null);
    const body = exportBody({ kind, detail, fields: chosen, excel }, registrations);
    try {
      if (kind === "csv") {
        const outcome = await downloadResultsCsv(contestId, body);
        if (outcome.kind === "refused") {
          setFailure({ where: "download", message: messageForCode(outcome.code, dict.errors) });
          return;
        }
        saveBlob(outcome.blob, outcome.fileName);
        return;
      }

      const outcome = await fetchResultsReport(contestId, body);
      if (outcome.kind === "refused") {
        setFailure({ where: "download", message: messageForCode(outcome.code, dict.errors) });
        return;
      }
      const { makeResultsPdf } = await import("./pdf/make-results-pdf");
      const blob = await makeResultsPdf(kind, {
        report: outcome.report,
        language,
        texts: pdfTexts[language],
        installationName,
        timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
        ...(kind === "protocol" ? { jury: { chair: chair.trim(), members: namesOf(members) } } : {}),
        logoHref,
      });
      saveBlob(blob, resultsFileName(kind, contestId, outcome.report.generatedAt));
    } catch {
      setFailure({ where: "download", message: t.failed });
    } finally {
      setBusy(null);
    }
  }

  function toggleField(field: ResultsField) {
    setFields((previous) => {
      const next = new Set(previous);
      if (!next.delete(field)) next.add(field);
      return next;
    });
  }

  const formatHint = `${ids}-format-hint`;
  const protocolReason = `${ids}-protocol-reason`;
  const detailHint = `${ids}-detail-hint`;
  const excelHint = `${ids}-excel-hint`;
  const downloadHint = `${ids}-download-hint`;
  const downloadLabel = busy === "download" ? t.preparing : t.download[kind].replace("{n}", String(count));

  return (
    <div className="flex flex-col gap-6">
      <Warnings report={report} t={t} />

      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <p className="font-mono text-data text-ink-3">{t.asOf.replace("{time}", formatTime(report.generatedAt, { locale }))}</p>
        <Button type="button" variant="quiet" size="sm" onClick={refresh} disabled={busy !== null} aria-busy={busy === "refresh"}>
          <RefreshCw strokeWidth={1.75} aria-hidden />
          {busy === "refresh" ? t.refreshing : t.refresh}
        </Button>
        {failure?.where === "refresh" ? (
          <p role="alert" className="text-small text-bad">
            {failure.message}
          </p>
        ) : null}
      </div>

      {/* The options come first in reading order, which a one-column form
          shows first; a wide form puts the list on the left as the mockup does.
          The form's own width decides, since the contest's sections take part
          of the viewport. While it is one column the download bar is pinned to
          the viewport, and the bottom padding keeps the list's end clear of
          it; the padding sits inside the container, which cannot query itself. */}
      <div className="@container/export">
        <div className="grid grid-cols-1 gap-8 @max-3xl/export:pb-36 @3xl/export:grid-cols-[minmax(0,1fr)_20rem] @3xl/export:gap-10">
          <div className="flex min-w-0 flex-col gap-5 @3xl/export:sticky @3xl/export:top-6 @3xl/export:col-start-2 @3xl/export:row-start-1 @3xl/export:self-start">
            <fieldset className="flex min-w-0 flex-col gap-2">
              <legend className="mb-2 font-mono text-label text-ink-3 uppercase">{t.format}</legend>
              <Segments>
                {KINDS.map((value) => {
                  const unavailable = value === "protocol" && !protocolOffered;
                  return (
                    <Segment key={value}>
                      <input
                        type="radio"
                        name={`${ids}-format`}
                        value={value}
                        checked={kind === value}
                        disabled={unavailable}
                        onChange={() => setKind(value)}
                        aria-describedby={unavailable ? protocolReason : formatHint}
                        className="sr-only"
                      />
                      <span className="truncate">{t.formats[value]}</span>
                    </Segment>
                  );
                })}
              </Segments>
              <p id={formatHint} className="text-small text-ink-3 max-narrow:hidden">
                {t.formatHints[kind]}
              </p>
              {!protocolOffered ? (
                <p id={protocolReason} className="text-small text-ink-3">
                  {t.protocolUnavailable}
                </p>
              ) : null}
            </fieldset>

            <fieldset className="flex min-w-0 flex-col gap-2">
              <legend className="mb-2 font-mono text-label text-ink-3 uppercase">{t.detail}</legend>
              <Segments>
                {RESULTS_DETAILS.map((value) => (
                  <Segment key={value}>
                    <input
                      type="radio"
                      name={`${ids}-detail`}
                      value={value}
                      checked={detail === value}
                      onChange={() => setDetail(value)}
                      aria-describedby={detailHint}
                      className="sr-only"
                    />
                    <span className="truncate">{t.details[value]}</span>
                  </Segment>
                ))}
              </Segments>
              <p id={detailHint} className="text-small text-ink-3 max-narrow:hidden">
                {t.detailHints[detail]}
              </p>
            </fieldset>

            <fieldset className="flex min-w-0 flex-col">
              <legend className="mb-2 font-mono text-label text-ink-3 uppercase">{t.identity}</legend>
              <div className="flex flex-wrap gap-x-5 gap-y-2">
                {offered.map((field) => {
                  const on = chosen.has(field);
                  return (
                    <label key={field} className="flex cursor-pointer items-center gap-2 text-small text-ink">
                      <Checkbox
                        checked={on}
                        // The export refuses a file that names nobody, so the last column stays.
                        disabled={on && chosen.size === 1}
                        onCheckedChange={() => toggleField(field)}
                      />
                      {t.fields[field]}
                    </label>
                  );
                })}
              </div>
            </fieldset>

            {kind === "csv" ? (
              <div className="flex flex-col gap-1.5">
                <label className="flex cursor-pointer items-center gap-2 text-small text-ink">
                  <Checkbox
                    checked={excel}
                    onCheckedChange={(value) => setExcel(value)}
                    aria-describedby={excelHint}
                  />
                  {t.excel}
                </label>
                <p id={excelHint} className="text-small text-ink-3 max-narrow:hidden">
                  {t.excelHint}
                </p>
              </div>
            ) : null}

            {pdf ? (
              <div className="flex flex-wrap items-center gap-3">
                <Label htmlFor={`${ids}-language`}>{t.language}</Label>
                <select
                  id={`${ids}-language`}
                  value={language}
                  onChange={(event) => setLanguage(event.target.value as Locale)}
                  className={cn(
                    "h-(--control-h) rounded-none border border-edge bg-transparent px-3",
                    "text-control text-ink transition-colors duration-(--t-input) ease-standard",
                    "hover:border-ink-2 focus-visible:border-ink",
                  )}
                >
                  {LOCALES.map((code) => (
                    <option key={code} value={code}>
                      {LOCALE_NAMES[code]}
                    </option>
                  ))}
                </select>
              </div>
            ) : null}

            {kind === "protocol" ? (
              <fieldset className="flex min-w-0 flex-col gap-4">
                <legend className="mb-2 font-mono text-label text-ink-3 uppercase">{t.jury}</legend>
                <Field id={`${ids}-chair`} label={t.chair}>
                  <Input value={chair} autoComplete="off" onChange={(event) => setChair(event.target.value)} />
                </Field>
                <Field id={`${ids}-members`} label={t.members} hint={t.membersHint}>
                  <Textarea
                    value={members}
                    onChange={(event) => setMembers(event.target.value)}
                    className="min-h-24 font-sans text-control"
                  />
                </Field>
              </fieldset>
            ) : null}

            {/* One column, the button is a bar on the viewport's bottom edge, so
                it is reachable without scrolling back past the list. */}
            <div
              className={cn(
                "flex flex-col gap-2 border-t border-line-2 pt-4",
                "@max-3xl/export:fixed @max-3xl/export:inset-x-0 @max-3xl/export:bottom-0 @max-3xl/export:z-10 @max-3xl/export:bg-bg",
                "@max-3xl/export:px-4.5 @max-3xl/export:pt-3 @max-3xl/export:pb-[max(0.75rem,env(safe-area-inset-bottom))]",
                // Wider than a phone, the bar is a strip with the button at its end rather than a slab across the screen.
                "@max-3xl/export:narrow:flex-row @max-3xl/export:narrow:items-center @max-3xl/export:narrow:justify-end @max-3xl/export:narrow:gap-4 @max-3xl/export:narrow:px-10",
              )}
            >
              {failure?.where === "download" ? (
                <p role="alert" className="text-small text-bad">
                  {failure.message}
                </p>
              ) : null}
              {count === 0 ? (
                <p id={downloadHint} className="text-small text-ink-3">
                  {t.tickOne}
                </p>
              ) : null}
              <Button
                type="button"
                onClick={download}
                disabled={count === 0 || busy !== null}
                aria-busy={busy === "download"}
                aria-describedby={count === 0 ? downloadHint : undefined}
                className="w-full @max-3xl/export:narrow:w-auto"
              >
                <FileDown strokeWidth={1.75} aria-hidden />
                {downloadLabel}
              </Button>
            </div>
          </div>

          <div className="min-w-0 @3xl/export:col-start-1 @3xl/export:row-start-1">
            <ParticipantList
              rows={report.rows}
              scoring={report.contest.scoring}
              selected={selected}
              onChange={setSelected}
              dict={dict}
            />
          </div>
        </div>
      </div>
    </div>
  );
}

/** One name per line, blank lines dropped. */
function namesOf(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

/** The bands a file's reader must know before sharing it, as the report's state calls for them. */
function Warnings({ report, t }: { report: ResultsReport; t: Dictionary["leaderboard"]["export"] }) {
  return (
    <div className="flex flex-col gap-2 empty:hidden">
      {report.status === "preliminary" ? (
        <p className="bg-warn-wash px-3 py-2 text-small text-warn">{t.preliminary}</p>
      ) : null}
      {report.status === "final" ? <p className="bg-warn-wash px-3 py-2 text-small text-warn">{t.notArchived}</p> : null}
      {report.hiddenByFreeze ? <p className="bg-frost-wash px-3 py-2 text-small text-frost">{t.hiddenByFreeze}</p> : null}
    </div>
  );
}

/** A row of mutually exclusive choices drawn as one pill, each choice a native radio. */
function Segments({ children }: { children: React.ReactNode }) {
  return <div className="grid auto-cols-fr grid-flow-col rounded-full border border-edge p-0.5">{children}</div>;
}

function Segment({ children }: { children: React.ReactNode }) {
  return (
    <label
      className={cn(
        "relative flex h-8 min-w-0 cursor-pointer items-center justify-center rounded-full px-2 text-control-sm text-ink-2",
        "transition-colors duration-(--t-input) ease-standard",
        "has-checked:bg-cta has-checked:text-cta-fg",
        "has-disabled:cursor-not-allowed has-disabled:opacity-45",
        "has-focus-visible:outline-2 has-focus-visible:outline-offset-2 has-focus-visible:outline-accent",
      )}
    >
      {children}
    </label>
  );
}
