import type { ProfileReport, ProfileReportQuestion, ProfileReportResult } from "@/lib/api/profile";
import { readableDuration } from "@/lib/format/bytes";
import { formatTime } from "@/lib/format/datetime";

import type { ReportDict } from "./report-tabs";

/**
 * The report's first tab: what one participant's contest came to.
 *
 * Their own numbers, then the place — and the place only when the table is
 * open. The freeze is not worked around here: `placeOpen` is the server's own
 * decision (`leaderboard.Decide`), and a screen that printed a place beside
 * it would be a second door past the rule the standings screen keeps.
 *
 * There are two ways to have no place, and they are not the same sentence. A
 * frozen table has not published one yet; a winner-mode contest places its
 * winner and nobody else, on purpose and for good. Saying "not yet" to the
 * second would be promising something that is never coming.
 *
 * Definitions rather than tiles, a rule between the sections and mono figures
 * with tabular numerals (SPEC §5): the same vocabulary the profile screen
 * above it is written in.
 */
export function ResultSummary({
  report,
  t,
  locale,
}: {
  report: ProfileReport;
  t: ReportDict;
  locale: string;
}) {
  const result = report.result;
  const icpc = result.scoring === "icpc";

  return (
    <div className="flex min-w-0 flex-col gap-8">
      {report.disqualified ? (
        <p className="max-w-body text-body text-bad">{t.disqualified}</p>
      ) : null}

      <section aria-labelledby="report-result" className="flex flex-col gap-5">
        <h2 id="report-result" className="font-mono text-label text-ink-3 uppercase">
          {t.result.heading}
        </h2>

        <dl className="grid grid-cols-4 gap-x-6 gap-y-7 max-narrow:grid-cols-2">
          {icpc ? (
            <>
              <Figure label={t.result.solved} value={String(result.solved)} />
              <Figure label={t.result.penalty} value={String(result.penalty ?? 0)} />
            </>
          ) : (
            <>
              <Figure label={t.result.points} value={String(result.points)} />
              <Figure label={t.result.solved} value={String(result.solved)} />
            </>
          )}
          <Figure
            label={t.result.worked}
            value={report.workedMs === undefined ? t.result.noWorked : readableDuration(report.workedMs / 1000)}
          />
          <Figure label={t.result.queries} value={String(report.queries)} />
          <Figure label={t.result.successful} value={String(report.successfulQueries)} />
          <Place result={result} t={t} />
        </dl>

        <Standing result={result} t={t} />
        {report.startedAt === undefined ? (
          <p className="max-w-body text-body text-ink-3">{t.result.notStarted}</p>
        ) : null}
      </section>

      <Questions questions={report.questions} icpc={icpc} truncated={report.truncated} t={t} locale={locale} />
    </div>
  );
}

/**
 * The place, and only where there is one. A row the table did not place gets
 * no figure at all rather than a dash: the sentence under the strip says
 * which of the two reasons it is.
 */
function Place({ result, t }: { result: ProfileReportResult; t: ReportDict }) {
  if (!result.placeOpen || result.place === null) return null;

  return (
    <div className="flex min-w-0 flex-col-reverse gap-1">
      <dt className="font-mono text-label text-ink-3 uppercase">{t.result.place}</dt>
      <dd className="flex items-baseline gap-2">
        <span className="font-mono text-h3 text-ink tabular-nums">{result.place}</span>
        {result.participants !== null ? (
          <span className="font-mono text-data text-ink-3 tabular-nums">
            {t.result.placeOf.replace("{n}", String(result.participants))}
          </span>
        ) : null}
      </dd>
    </div>
  );
}

/** The line under the strip: why there is a place, or why there is not. */
function Standing({ result, t }: { result: ProfileReportResult; t: ReportDict }) {
  if (!result.placeOpen) return <p className="max-w-body text-body text-ink-3">{t.result.placePending}</p>;
  if (result.winner) return <p className="max-w-body text-body text-good">{t.result.winner}</p>;
  if (result.place === null) return <p className="max-w-body text-body text-ink-3">{t.result.unplaced}</p>;
  if (result.truncated) {
    return (
      <p className="max-w-body text-small text-warn">
        {t.result.truncated.replace("{n}", String(result.participants ?? 0))}
      </p>
    );
  }
  return null;
}

/**
 * The questions, in order: solved or not, how many attempts, when it was
 * first right, and what it was worth. What a participant already knows about
 * themselves, gathered in one place.
 *
 * A real table, with its own horizontal scroll, so five columns at 375px
 * scroll inside the box rather than pushing the page sideways.
 */
function Questions({
  questions,
  icpc,
  truncated,
  t,
  locale,
}: {
  questions: ProfileReportQuestion[];
  icpc: boolean;
  truncated: boolean;
  t: ReportDict;
  locale: string;
}) {
  return (
    <section aria-labelledby="report-questions" className="flex min-w-0 flex-col gap-5 border-t border-line pt-6">
      <h2 id="report-questions" className="font-mono text-label text-ink-3 uppercase">
        {t.questions.heading}
      </h2>

      {questions.length === 0 ? (
        <p className="max-w-body text-body text-ink-2">{t.questions.empty}</p>
      ) : (
        <>
          <div className="min-w-0 overflow-x-auto">
            <table aria-label={t.questions.heading} className="w-full min-w-[34rem] border-collapse text-left">
              <thead>
                <tr className="border-b border-line">
                  <Head>{t.questions.columns.question}</Head>
                  <Head>{t.questions.columns.verdict}</Head>
                  <Head numeric>{t.questions.columns.attempts}</Head>
                  <Head numeric>{t.questions.columns.firstSolved}</Head>
                  <Head numeric>{icpc ? t.questions.columns.penalty : t.questions.columns.points}</Head>
                </tr>
              </thead>
              <tbody>
                {questions.map((question) => (
                  <tr key={question.questionId} className="border-b border-line">
                    <Cell>{question.ord}</Cell>
                    <td className="py-2.5 pr-4">
                      <span className={question.solved ? "text-body text-good" : "text-body text-ink-3"}>
                        {question.solved ? t.questions.solved : t.questions.unsolved}
                      </span>
                    </td>
                    <Cell numeric>{question.attempts}</Cell>
                    <Cell numeric>
                      {question.solvedAt ? formatTime(question.solvedAt, { locale }) : t.questions.never}
                    </Cell>
                    {/* The column's own number: minutes where the mode
                        charges them, points where it awards them. ICPC
                        awards none, so printing points there would be a
                        column of noughts under a real total. */}
                    <Cell numeric>{icpc ? question.penalty : question.points}</Cell>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {truncated ? (
            <p className="max-w-body text-small text-warn">
              {/* The attempts counted, not the questions: the bound the
                  report was cut at is a number of attempts, and two
                  questions holding fifty of them are not "two". */}
              {t.questions.truncated.replace(
                "{n}",
                String(questions.reduce((sum, question) => sum + question.attempts, 0)),
              )}
            </p>
          ) : null}
        </>
      )}
    </section>
  );
}

function Head({ children, numeric = false }: { children: React.ReactNode; numeric?: boolean }) {
  return (
    <th
      scope="col"
      className={`py-2 pr-4 font-mono text-label font-normal text-ink-3 uppercase ${numeric ? "text-right" : ""}`}
    >
      {children}
    </th>
  );
}

function Cell({ children, numeric = false }: { children: React.ReactNode; numeric?: boolean }) {
  return (
    <td className={`py-2.5 pr-4 font-mono text-data text-ink ${numeric ? "text-right tabular-nums" : ""}`}>
      {children}
    </td>
  );
}

/**
 * One number over its caption. The term comes first in the markup, because
 * that is what a definition list is; the column is reversed for the eye.
 */
function Figure({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex min-w-0 flex-col-reverse gap-1">
      <dt className="font-mono text-label text-ink-3 uppercase">{label}</dt>
      <dd className="font-mono text-h3 text-ink tabular-nums">{value}</dd>
    </div>
  );
}
