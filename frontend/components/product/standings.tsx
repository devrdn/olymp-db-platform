import type { Prize, Standings, StandingsCell, StandingsRow } from "@/lib/api/leaderboard";
import { identityHue } from "@/lib/api/leaderboard";
import { formatTime } from "@/lib/format/datetime";
import { initials } from "@/lib/format/initials";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The standings as participants and public-link visitors see them: a state
 * banner, a podium on the full page, and the table. Presentational, so the
 * play tab and the public page share it.
 *
 * The one place the standings sub-palette is spent (SPEC §3.5). No colour is
 * the only carrier of a fact: the medal holds its prize's numeral and names it
 * in words, the freeze has its sentence, your row says "You".
 */

/** The part of the dictionary the standings read. */
export type StandingsDictionary = Pick<Dictionary, "leaderboard">;

type Medal = "gold" | "silver" | "bronze";

const MEDALS: Record<Prize, Medal> = { 1: "gold", 2: "silver", 3: "bronze" };

const NUMERALS: Record<Prize, string> = { 1: "I", 2: "II", 3: "III" };

// Written out whole so Tailwind finds every class. `row` tints a medal's whole
// row; your own row's tint wins over it.
const MEDAL_CLASSES: Record<
  Medal,
  { disc: string; bar: string; edge: string; step: string; row: string }
> = {
  gold: {
    disc: "bg-gold-fill text-gold-on ring-gold-fill/40",
    bar: "bg-gold-fill",
    edge: "border-l-gold-fill",
    step: "bg-gold-wash border-t-gold-fill",
    row: "bg-gold-wash",
  },
  silver: {
    disc: "bg-silver-fill text-silver-on ring-silver-fill/40",
    bar: "bg-silver-fill",
    edge: "border-l-silver-fill",
    step: "bg-silver-wash border-t-silver-fill",
    row: "bg-silver-wash",
  },
  bronze: {
    disc: "bg-bronze-fill text-bronze-on ring-bronze-fill/40",
    bar: "bg-bronze-fill",
    edge: "border-l-bronze-fill",
    step: "bg-bronze-wash border-t-bronze-fill",
    row: "bg-bronze-wash",
  },
};

const HUE_CLASSES: Record<number, { disc: string; bar: string }> = {
  1: { disc: "bg-id-1-wash text-id-1", bar: "bg-id-1" },
  2: { disc: "bg-id-2-wash text-id-2", bar: "bg-id-2" },
  3: { disc: "bg-id-3-wash text-id-3", bar: "bg-id-3" },
  4: { disc: "bg-id-4-wash text-id-4", bar: "bg-id-4" },
  5: { disc: "bg-id-5-wash text-id-5", bar: "bg-id-5" },
  6: { disc: "bg-id-6-wash text-id-6", bar: "bg-id-6" },
};

function medalOf(prize: Prize | null): Medal | undefined {
  return prize === null ? undefined : MEDALS[prize];
}

/** The left edge a prize row carries, for tables other than StandingsView. */
export function medalEdge(prize: Prize | null): string {
  const medal = medalOf(prize);
  return medal ? MEDAL_CLASSES[medal].edge : "border-l-transparent";
}

type PrizeNames = StandingsDictionary["leaderboard"]["prizes"];

function prizeName(prize: Prize | null, names: PrizeNames): string {
  switch (prize) {
    case 1:
      return names.first;
    case 2:
      return names.second;
    case 3:
      return names.third;
    case null:
      return names.none;
  }
}

/** Whether any row of the table holds a prize; only then does the table carry the column. */
export function hasPrize(rows: readonly { prize: Prize | null }[]): boolean {
  return rows.some((row) => row.prize !== null);
}

/**
 * A row's place as a number, or a dash for a participant who has not answered.
 * Ties share a number; the server decides both.
 */
export function PlaceBadge({ place, unplaced }: { place: number | null; unplaced: string }) {
  return (
    <span data-testid="place" className="inline-flex">
      {place === null ? (
        <>
          <span aria-hidden className="font-mono text-data text-ink-3">
            —
          </span>
          <span className="sr-only">{unplaced}</span>
        </>
      ) : (
        <span className="font-mono text-data text-ink-2 tabular-nums">{place}</span>
      )}
    </span>
  );
}

/** A row's prize tier as a medal with its numeral, or a dash for none. */
export function PrizeBadge({ prize, names }: { prize: Prize | null; names: PrizeNames }) {
  return (
    <span data-testid="prize" data-medal={medalOf(prize)} className="inline-flex">
      {prize !== null ? (
        <span
          aria-hidden
          className={cn(
            "grid size-7 place-items-center rounded-full font-mono text-label font-medium ring-2 ring-offset-1 ring-offset-bg",
            MEDAL_CLASSES[MEDALS[prize]].disc,
          )}
        >
          {NUMERALS[prize]}
        </span>
      ) : (
        <span aria-hidden className="grid size-7 place-items-center font-mono text-data text-ink-3">
          —
        </span>
      )}
      <span className="sr-only">{prizeName(prize, names)}</span>
    </span>
  );
}

export function Initials({ label, deleted }: { label: string; deleted: boolean }) {
  const hue = identityHue(label);
  return (
    <span
      aria-hidden
      data-testid="initials"
      data-hue={deleted ? undefined : hue}
      className={cn(
        "grid size-8 shrink-0 place-items-center rounded-full font-mono text-label uppercase",
        deleted ? "bg-sunk text-ink-3" : HUE_CLASSES[hue].disc,
      )}
    >
      {deleted ? "" : initials(label)}
    </span>
  );
}

function ended(endsAt: string | undefined): boolean {
  return endsAt !== undefined && Date.parse(endsAt) <= Date.now();
}

type CellDictionary = StandingsDictionary["leaderboard"]["cells"];

/** The question's letter and its state in words, never colour alone (SPEC §3.5). */
function cellAccessibleName(cell: StandingsCell, letter: string, t: CellDictionary): string {
  switch (cell.state) {
    case "solved": {
      const attempt = cell.attempts ?? 1;
      const template = cell.first ? t.solvedFirst : t.solved;
      return template
        .replace("{letter}", letter)
        .replace("{minute}", String(cell.minute ?? 0))
        .replace("{attempt}", String(attempt));
    }
    case "failed":
      return t.failed.replace("{letter}", letter).replace("{n}", String(cell.attempts ?? 0));
    case "pending": {
      const n = String(cell.pending ?? 0);
      const wrong = cell.attempts ?? 0;
      // Wrong attempts made before the freeze stay visible: they were public
      // already, so this leaks nothing.
      return wrong > 0
        ? t.pendingWrong.replace("{letter}", letter).replace("{n}", n).replace("{w}", String(wrong))
        : t.pending.replace("{letter}", letter).replace("{n}", n);
    }
    case "untried":
      return t.untried.replace("{letter}", letter);
  }
}

/**
 * The ICPC grid's fixed columns: each Tailwind class next to the rem width
 * `gridMinWidthRem` sums, so the two cannot drift. Shared with
 * `staff-standings.tsx`.
 */
export const ICPC_COLUMN = {
  place: { className: "w-14", rem: 3.5 },
  placeWide: { className: "w-16", rem: 4 },
  prize: { className: "w-18", rem: 4.5 },
  login: { className: "w-28", rem: 7 },
  solved: { className: "w-20", rem: 5 },
  penalty: { className: "w-20", rem: 5 },
  /** `table-fixed` gives the body cells the header's width, so only the header needs the class. */
  grid: { className: "w-12", rem: 3 },
} as const;

/**
 * Floor for the name column. With many questions the fixed columns alone exceed
 * the viewport and the name would collapse to nothing; past that point the
 * table scrolls instead.
 */
const GRID_NAME_MIN_REM = 12;

/**
 * Minimum table width in rem with the ICPC grid shown: fixed columns, one cell
 * per question, and the name floor.
 */
export function gridMinWidthRem(fixedColumnsRem: number, questionCount: number): number {
  return fixedColumnsRem + questionCount * ICPC_COLUMN.grid.rem + GRID_NAME_MIN_REM;
}

const GRID_MIN_WIDTH_VAR = "--grid-min-width";

/**
 * Class and style that protect the name column while the ICPC grid is shown, or
 * `undefined` when there is no grid. The width is scoped to `narrow` and up,
 * where the grid appears: a phone never shows it and must not scroll sideways
 * for it.
 */
export function gridTableWidth(
  fixedColumnsRem: number,
  questionCount: number | undefined,
): { className: string; style: React.CSSProperties } | undefined {
  if (!questionCount) return undefined;
  return {
    // Written out whole, not built from GRID_MIN_WIDTH_VAR, so Tailwind's scan
    // finds it.
    className: "narrow:min-w-(--grid-min-width)",
    style: { [GRID_MIN_WIDTH_VAR]: `${gridMinWidthRem(fixedColumnsRem, questionCount)}rem` } as React.CSSProperties,
  };
}

/**
 * The ICPC grid's header row, one cell per question; shown only on the full
 * page from `narrow` up. The play tab and phones get the plain "solved,
 * penalty" columns.
 */
export function GridHeaderCells({ questions }: { questions: string[] }) {
  return (
    <>
      {questions.map((letter) => (
        <th
          key={letter}
          scope="col"
          className={cn(ICPC_COLUMN.grid.className, "px-1 py-2 text-center font-mono font-normal max-narrow:hidden")}
        >
          {letter}
        </th>
      ))}
    </>
  );
}

/**
 * One row's ICPC cells. `solved` shows the attempt above the minute ("+" first
 * try, "+N" after N wrong), a first solve as a solid fill; `failed` shows the
 * wrong count; `pending` shows "?" with attempts after the freeze, plus "−N"
 * for wrong attempts known before it; `untried` shows nothing.
 */
export function GridCells({
  cells,
  questions,
  dict,
}: {
  cells: StandingsCell[];
  questions: string[];
  dict: StandingsDictionary;
}) {
  const t = dict.leaderboard.cells;
  return (
    <>
      {cells.map((cell, i) => {
        const letter = questions[i] ?? "";
        const name = cellAccessibleName(cell, letter, t);
        const attempt = cell.attempts ?? 1;
        return (
          <td
            key={letter || i}
            data-testid="cell"
            data-state={cell.state}
            className={cn(
              "px-1 py-2 text-center align-middle font-mono text-label tabular-nums max-narrow:hidden",
              cell.state === "solved" && (cell.first ? "bg-good text-bg" : "bg-good-wash text-good"),
              cell.state === "failed" && "bg-bad-wash text-bad",
              cell.state === "pending" && "bg-warn-wash text-warn",
            )}
          >
            <span className="sr-only">{name}</span>
            <div aria-hidden className="flex flex-col items-center leading-tight">
              {cell.state === "solved" ? (
                <>
                  <span>{attempt > 1 ? `+${attempt - 1}` : "+"}</span>
                  <span>{cell.minute}</span>
                </>
              ) : cell.state === "failed" ? (
                <span>{`−${cell.attempts ?? 0}`}</span>
              ) : cell.state === "pending" ? (
                <>
                  <span>?</span>
                  <span>{cell.pending ?? 0}</span>
                  {(cell.attempts ?? 0) > 0 ? <span>{`−${cell.attempts}`}</span> : null}
                </>
              ) : null}
            </div>
          </td>
        );
      })}
    </>
  );
}

export function StandingsView({
  standings,
  dict,
  locale,
  variant,
  failed = false,
}: {
  standings: Standings;
  dict: StandingsDictionary;
  locale: string;
  /** `panel` is the narrow play tab; `page` is the public page with its podium. */
  variant: "panel" | "page";
  /** The last refresh failed; the previous copy is shown. */
  failed?: boolean;
}) {
  const t = dict.leaderboard;
  const { state, rows } = standings;
  const icpc = standings.scoring === "icpc";
  const leader = rows.reduce((max, r) => Math.max(max, r.points), 0);
  // The grid only appears on `page`.
  const gridQuestions = variant === "page" && icpc ? standings.questions : undefined;
  const prizes = hasPrize(rows);
  const gridWidth = gridTableWidth(
    ICPC_COLUMN.place.rem +
      (prizes ? ICPC_COLUMN.prize.rem : 0) +
      ICPC_COLUMN.solved.rem +
      ICPC_COLUMN.penalty.rem,
    gridQuestions?.length,
  );

  return (
    <div className="flex flex-col gap-4">
      <Banner standings={standings} dict={dict} locale={locale} />

      {failed ? <p className="text-small text-warn">{t.failed}</p> : null}

      {state === "not_started" ? null : rows.length === 0 ? (
        <p className="text-body text-ink-2">{t.empty}</p>
      ) : (
        <>
          {variant === "page" ? (
            <Podium rows={rows} dict={dict} scoring={standings.scoring} />
          ) : null}
          <div className="overflow-x-auto">
            {/* Fixed layout, so the name column gives way instead of pushing the
               points off a phone. The `narrow` min-width keeps a wide ICPC grid
               from squeezing it to nothing. */}
            <table
              className={cn("w-full table-fixed border-collapse", gridWidth?.className)}
              style={gridWidth?.style}
            >
              <caption className="sr-only">{t.heading}</caption>
              <thead>
                <tr className="border-b border-line-2 font-mono text-label text-ink-3 uppercase">
                  <th
                    scope="col"
                    className={cn(ICPC_COLUMN.place.className, "py-2 pr-2 pl-3 text-left font-normal")}
                  >
                    {t.columns.place}
                  </th>
                  {prizes ? (
                    <th scope="col" className={cn(ICPC_COLUMN.prize.className, "px-2 py-2 text-left font-normal")}>
                      {t.columns.prize}
                    </th>
                  ) : null}
                  <th scope="col" className="px-2 py-2 text-left font-normal">
                    {t.columns.participant}
                  </th>
                  {icpc ? (
                    <>
                      {variant === "page" && standings.questions ? (
                        <GridHeaderCells questions={standings.questions} />
                      ) : null}
                      <th scope="col" className={cn(ICPC_COLUMN.solved.className, "px-2 py-2 text-right font-normal")}>
                        {t.columns.solved}
                      </th>
                      <th scope="col" className={cn(ICPC_COLUMN.penalty.className, "py-2 pr-3 pl-2 text-right font-normal")}>
                        {t.columns.penalty}
                      </th>
                    </>
                  ) : (
                    <>
                      <th
                        scope="col"
                        className={cn(
                          "px-2 py-2 text-right font-normal",
                          variant === "page" ? "w-44 max-narrow:w-24" : "w-24",
                        )}
                      >
                        {t.columns.points}
                      </th>
                      {variant === "page" ? (
                        <>
                          <th
                            scope="col"
                            className="w-24 px-2 py-2 text-right font-normal max-narrow:hidden"
                          >
                            {t.columns.solved}
                          </th>
                          <th
                            scope="col"
                            className="w-28 py-2 pr-3 pl-2 text-right font-normal max-narrow:hidden"
                          >
                            {t.columns.last}
                          </th>
                        </>
                      ) : null}
                    </>
                  )}
                </tr>
              </thead>
              <tbody>
                {rows.map((row, index) => (
                  <Row
                    key={index}
                    row={row}
                    leader={leader}
                    dict={dict}
                    locale={locale}
                    variant={variant}
                    icpc={icpc}
                    prizes={prizes}
                    questions={standings.questions}
                  />
                ))}
              </tbody>
            </table>
          </div>
          {standings.truncated ? (
            <p className="text-small text-warn">
              {t.truncated.replace("{count}", String(rows.length))}
            </p>
          ) : null}
        </>
      )}
    </div>
  );
}

function Row({
  row,
  leader,
  dict,
  locale,
  variant,
  icpc,
  prizes,
  questions,
}: {
  row: StandingsRow;
  leader: number;
  dict: StandingsDictionary;
  locale: string;
  variant: "panel" | "page";
  icpc: boolean;
  prizes: boolean;
  questions?: string[];
}) {
  const t = dict.leaderboard;
  const medal = medalOf(row.prize);
  // The ICPC branch has no score bar, so neither is needed there.
  const hue = icpc ? 0 : identityHue(row.label);
  const share = !icpc && leader > 0 ? Math.round((row.points / leader) * 100) : 0;

  return (
    <tr
      data-you={row.isYou ? "true" : undefined}
      className={cn(
        "border-b border-line",
        medal && MEDAL_CLASSES[medal].row,
        row.isYou && "bg-you-wash",
      )}
    >
      <td
        className={cn("border-l-3 py-2.5 pr-2 pl-3 align-middle", medalEdge(row.prize))}
      >
        <PlaceBadge place={row.place} unplaced={t.unplaced} />
      </td>
      {prizes ? (
        <td className="px-2 py-2.5 align-middle">
          <PrizeBadge prize={row.prize} names={t.prizes} />
        </td>
      ) : null}

      <td className="px-2 py-2.5 align-middle">
        <div className="flex min-w-0 items-center gap-2.5">
          <Initials label={row.label} deleted={row.deleted} />
          <span
            className={cn(
              "min-w-0 truncate text-body",
              row.deleted ? "text-ink-3 italic" : "text-ink",
            )}
          >
            {row.deleted ? t.deleted : row.label}
          </span>
          {row.isYou ? (
            <span className="shrink-0 rounded-full bg-you px-2 py-0.5 font-mono text-label text-bg uppercase">
              {t.you}
            </span>
          ) : null}
          {row.winner ? (
            <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-gold-wash px-2 py-0.5 font-mono text-label text-gold uppercase">
              <StarIcon />
              {t.winner}
            </span>
          ) : null}
        </div>
      </td>

      {icpc ? (
        <>
          {variant === "page" && questions && row.cells ? (
            <GridCells cells={row.cells} questions={questions} dict={dict} />
          ) : null}
          <td className="px-2 py-2.5 text-right align-middle font-mono text-data text-ink tabular-nums">
            {row.solved}
          </td>
          <td className="py-2.5 pr-3 pl-2 text-right align-middle font-mono text-data text-ink tabular-nums">
            {row.penalty}
          </td>
        </>
      ) : (
        <>
          <td className="px-2 py-2.5 text-right align-middle">
            <span className="font-mono text-data text-ink tabular-nums">
              {row.points}
            </span>
            <div
              className={cn(
                "mt-1.5 ml-auto h-1.5 max-w-full rounded-full bg-line-2",
                variant === "page" ? "w-36 max-narrow:w-16" : "w-16",
              )}
            >
              <div
                data-testid="bar"
                className={cn(
                  "h-full rounded-full",
                  medal
                    ? MEDAL_CLASSES[medal].bar
                    : row.deleted
                      ? "bg-ink-3"
                      : HUE_CLASSES[hue].bar,
                )}
                style={{ width: `${share}%` }}
              />
            </div>
          </td>

          {variant === "page" ? (
            <>
              <td className="px-2 py-2.5 text-right align-middle font-mono text-data text-ink-2 tabular-nums max-narrow:hidden">
                {row.solved}
              </td>
              <td className="py-2.5 pr-3 pl-2 text-right align-middle font-mono text-data text-ink-3 tabular-nums max-narrow:hidden">
                {row.lastScoredAt ? formatTime(row.lastScoredAt, { locale }) : "—"}
              </td>
            </>
          ) : null}
        </>
      )}
    </tr>
  );
}

/** The podium's steps left to right: second, first, third (SPEC §15). */
const PODIUM_STEPS: { prize: Prize; height: string }[] = [
  { prize: 2, height: "h-20" },
  { prize: 1, height: "h-28" },
  { prize: 3, height: "h-14" },
];

/**
 * One step per prize tier, each at its tier's height, so equal prizes never
 * look ranked and a lone third prize stands on the low step. A step names the
 * tier's first row and how many more share it; a tier nobody holds is an
 * empty step.
 */
function Podium({
  rows,
  dict,
  scoring,
}: {
  rows: StandingsRow[];
  dict: StandingsDictionary;
  scoring: Standings["scoring"];
}) {
  const icpc = scoring === "icpc";
  const tiers = PODIUM_STEPS.map((step) => ({ ...step, holders: rows.filter((r) => r.prize === step.prize) }));
  if (tiers.every((tier) => tier.holders.length === 0)) return null;

  return (
    <div
      data-testid="podium"
      aria-hidden
      className="grid grid-cols-3 items-end gap-3 pt-2 max-narrow:hidden"
    >
      {tiers.map(({ prize, height, holders }) => {
        const [row] = holders;
        if (!row) return <div key={prize} data-testid="podium-step" />;
        const medal = MEDALS[prize];
        return (
          <div key={prize} data-testid="podium-step" data-tier={prize} className="flex min-w-0 flex-col items-center gap-2">
            <span
              className={cn(
                "grid size-14 place-items-center rounded-full font-mono text-h3 ring-4 ring-offset-2 ring-offset-bg",
                MEDAL_CLASSES[medal].disc,
              )}
            >
              {NUMERALS[prize]}
            </span>
            <span className="max-w-full truncate text-body text-ink">
              {row.deleted ? dict.leaderboard.deleted : row.label}
            </span>
            <span className="font-mono text-data text-ink-2 tabular-nums">
              {icpc ? `${row.solved} · ${row.penalty}` : row.points}
            </span>
            {holders.length > 1 ? (
              <span className="text-small text-ink-3">
                {dict.leaderboard.podiumMore.replace("{count}", String(holders.length - 1))}
              </span>
            ) : null}
            <div className={cn("w-full border-t-4", height, MEDAL_CLASSES[medal].step)} />
          </div>
        );
      })}
    </div>
  );
}

function Banner({
  standings,
  dict,
  locale,
}: {
  standings: Standings;
  dict: StandingsDictionary;
  locale: string;
}) {
  const t = dict.leaderboard;
  const updated = t.updated.replace(
    "{time}",
    formatTime(standings.generatedAt, { locale }),
  );

  switch (standings.state) {
    case "live":
      return (
        <div
          role="status"
          className="flex flex-wrap items-center gap-x-3 gap-y-1 border border-accent/25 bg-accent-wash px-3 py-2"
        >
          <span className="inline-flex items-center gap-2 font-mono text-label text-accent uppercase">
            <span
              aria-hidden
              className="size-1.5 rounded-full bg-accent motion-safe:animate-live"
            />
            {t.live}
          </span>
          <span className="font-mono text-label text-accent">{updated}</span>
        </div>
      );
    case "frozen": {
      const at = standings.frozenAt
        ? formatTime(standings.frozenAt, { locale })
        : "";
      return (
        <div
          role="status"
          className="flex items-start gap-3 border border-frost/30 bg-frost-wash px-3 py-2.5 text-frost"
        >
          <SnowflakeIcon />
          <div className="flex flex-col gap-0.5">
            <span className="font-mono text-label uppercase">
              {t.frozen.replace("{time}", at)}
            </span>
            <span className="text-small">
              {ended(standings.endsAt) ? t.frozenOver : t.frozenBody}
            </span>
          </div>
        </div>
      );
    }
    case "final":
      return (
        <div
          role="status"
          className="flex flex-wrap items-center gap-x-3 gap-y-1 border border-gold/35 bg-gold-wash px-3 py-2 text-gold"
        >
          <span className="inline-flex items-center gap-2 font-mono text-label uppercase">
            <StarIcon />
            {t.final}
          </span>
          <span className="font-mono text-label">{updated}</span>
        </div>
      );
    case "not_started":
      return (
        <p
          role="status"
          className="border border-line-2 bg-sunk px-3 py-2.5 text-body text-ink-2"
        >
          {t.notStarted}
        </p>
      );
  }
}

function SnowflakeIcon() {
  return (
    <svg
      aria-hidden
      viewBox="0 0 16 16"
      className="mt-0.5 size-4 shrink-0"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
    >
      <path d="M8 1.5v13M2.4 4.75l11.2 6.5M2.4 11.25l11.2-6.5" />
      <path d="M6.3 2.6 8 4.1l1.7-1.5M6.3 13.4 8 11.9l1.7 1.5" />
    </svg>
  );
}

function StarIcon() {
  return (
    <svg
      aria-hidden
      viewBox="0 0 16 16"
      className="size-3.5 shrink-0"
      fill="currentColor"
    >
      <path d="M8 1.2l1.9 4.1 4.5.5-3.4 3 1 4.4L8 11l-4 2.2 1-4.4-3.4-3 4.5-.5z" />
    </svg>
  );
}
