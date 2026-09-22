import { Band } from "@/components/layout/band";
import { SqlBlock } from "@/components/product/sql-block";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * The statement in the frame: a question a detective game actually asks.
 *
 * Short enough to be read in one glance and plain enough to be guessed by
 * somebody who has never written SQL — everyone with no alibi, the most
 * recently seen first. A statement that showed off a window function would
 * demonstrate the language rather than the product.
 */
export const PREVIEW_QUERY = `SELECT name, city, last_seen
FROM suspects
WHERE alibi IS NULL
ORDER BY last_seen DESC;`;

/** The columns the answer came back with, and the types under their names. */
export const PREVIEW_COLUMNS = ["name", "city", "last_seen"] as const;
const PREVIEW_TYPES = ["text", "text", "timestamp"] as const;

/** The answer: few enough rows to be read, enough of them to look like data. */
export const PREVIEW_ROWS = [
  ["Ionescu", "Chisinau", "1908-04-11 23:40"],
  ["Bercu", "Balti", "1908-04-11 22:05"],
  ["Zaharia", "Orhei", "1908-04-10 19:15"],
] as const;

/** What the meter over the table reports, fixed the way the rest of the still is. */
const PREVIEW_MS = "12";

/**
 * A look at the thing itself: the editor with a highlighted statement and the
 * answer under it, standing still.
 *
 * **Not a screenshot, deliberately** (design §2.5 asks for "a frame of the
 * product"). A screenshot needs a running stack to take, becomes stale the
 * first time the console is touched, and has to exist twice because the
 * product has two themes. This is a replica assembled from the console's own
 * parts — `SqlBlock` is the same highlighter an organiser reads a query log
 * with, and the meter row and the result table carry the classes
 * `ResultPanel` gives them — so it is true by construction, it follows the
 * theme by itself, and it adds no binary to the repository.
 *
 * **Inert, equally deliberately.** There is no run button, no textarea and
 * nothing that takes focus. A visitor who pressed a button here and got
 * nothing would have been told the product is on this page when it is behind
 * a sign-in; a picture that does not pretend to be usable tells the truth
 * about where they are.
 *
 * Below the layout's one breakpoint it is not shown at all. A console at
 * 375px is a column of clipped identifiers over a table one column wide,
 * which demonstrates nothing; the three sentences above it already say what
 * this screen does.
 */
export function ConsolePreview({ dict }: { dict: Dictionary }) {
  const t = dict.home.console;
  // The meter's own words, taken from the console rather than restated here:
  // the still has to read exactly like the screen it stands for.
  const meter = dict.participant.console.meter;

  return (
    <div className="max-narrow:hidden">
      <Band className="gap-7">
        <h2 className="text-h3 text-ink">{t.heading}</h2>

        {/* The frame sits against the right edge of the content column, with
            the sentence beside it — the arrangement the design asks for. The
            two of them stack between the breakpoints, where a 17rem column of
            prose beside a console leaves neither enough room. */}
        <div className="grid grid-cols-[minmax(0,17rem)_minmax(0,1fr)] gap-x-10 max-wide:grid-cols-1 max-wide:gap-y-6">
          <p className="text-body text-ink-2">{t.lede}</p>

          {/* A hairline instead of a shadow: the one rule this page draws
              anything with. */}
          <div className="flex min-w-0 flex-col border border-line">
            {/* Where the console keeps its open tab. The title is the question
                the statement asks, so the frame reads as somebody's work
                rather than as a specimen. */}
            <p className="truncate border-b border-line px-3 py-2 font-mono text-label text-ink-3 uppercase">
              {t.tab}
            </p>

            <SqlBlock sql={PREVIEW_QUERY} label={t.queryLabel} />

            {/* The run's own facts in one quiet line, as `ResultPanel` reports
                them — minus the download button, which would be a promise
                this page cannot keep. */}
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
