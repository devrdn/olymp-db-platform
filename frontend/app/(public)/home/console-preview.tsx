import { Band } from "@/components/layout/band";
import { SqlBlock } from "@/components/product/sql-block";
import type { Dictionary } from "@/lib/i18n/dictionary";

/** The previewed statement: readable at a glance even without SQL. */
export const PREVIEW_QUERY = `SELECT name, city, last_seen
FROM suspects
WHERE alibi IS NULL
ORDER BY last_seen DESC;`;

/** The answer's columns. */
export const PREVIEW_COLUMNS = ["name", "city", "last_seen"] as const;
const PREVIEW_TYPES = ["text", "text", "timestamp"] as const;

/** The answer's rows. */
export const PREVIEW_ROWS = [
  ["Ionescu", "Chisinau", "1908-04-11 23:40"],
  ["Bercu", "Balti", "1908-04-11 22:05"],
  ["Zaharia", "Orhei", "1908-04-10 19:15"],
] as const;

/** The fixed timing shown in the meter. */
const PREVIEW_MS = "12";

/**
 * A still of the console assembled from its own parts (`SqlBlock`,
 * `ResultPanel` classes) rather than a screenshot, so it stays current,
 * follows the theme and adds no binary. Inert on purpose: the product is
 * behind sign-in. Hidden below the breakpoint, where it would show nothing
 * useful.
 */
export function ConsolePreview({ dict }: { dict: Dictionary }) {
  const t = dict.home.console;
  // The console's own meter wording.
  const meter = dict.participant.console.meter;

  return (
    <div className="max-narrow:hidden">
      <Band className="gap-7">
        <h2 className="text-h3 text-ink">{t.heading}</h2>

        {/* Frame on the right with the sentence beside it; stacked between breakpoints. */}
        <div className="grid grid-cols-[minmax(0,17rem)_minmax(0,1fr)] gap-x-10 max-wide:grid-cols-1 max-wide:gap-y-6">
          <p className="text-body text-ink-2">{t.lede}</p>

          {/* A hairline, not a shadow. */}
          <div className="flex min-w-0 flex-col border border-line">
            {/* The open tab's title is the question the statement asks. */}
            <p className="truncate border-b border-line px-3 py-2 font-mono text-label text-ink-3 uppercase">
              {t.tab}
            </p>

            <SqlBlock sql={PREVIEW_QUERY} label={t.queryLabel} />

            {/* The run's facts as `ResultPanel` shows them, without the download button. */}
            <div className="flex flex-wrap items-center gap-4 border-y border-line px-3 py-1.5 font-mono text-label text-ink-3 uppercase">
              <span className="flex items-center gap-1.5 text-good normal-case">
                <span aria-hidden="true" className="size-1.5 rounded-full bg-good" />
                {meter.ok}
              </span>
              <span>
                {meter.rows}{" "}
                <b className="font-medium text-ink tabular-nums">{PREVIEW_ROWS.length}</b>
              </span>
              <span>
                {meter.time}{" "}
                <b className="font-medium text-ink tabular-nums">
                  {meter.ms.replace("{n}", PREVIEW_MS)}
                </b>
              </span>
            </div>

            <div className="min-w-0 overflow-hidden p-3">
              <table className="w-full table-fixed border-collapse font-mono text-body">
                <thead>
                  <tr className="border-b border-edge">
                    {PREVIEW_COLUMNS.map((column, index) => (
                      <th
                        key={column}
                        className="truncate p-2 text-left align-bottom text-label text-ink-3 uppercase"
                      >
                        {column}
                        <span className="block truncate font-normal normal-case">
                          {PREVIEW_TYPES[index]}
                        </span>
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {PREVIEW_ROWS.map((row) => (
                    <tr key={row[0]} className="border-b border-line last:border-b-0">
                      {row.map((cell) => (
                        <td key={cell} className="truncate p-2 align-top text-ink">
                          {cell}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      </Band>
    </div>
  );
}
