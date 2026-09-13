import type { Standings, StandingsRow } from "@/lib/api/leaderboard";
import { identityHue } from "@/lib/api/leaderboard";
import { formatTime } from "@/lib/format/datetime";
import { initials } from "@/lib/format/initials";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The contest's table, as a participant or anybody with the public link sees
 * it: a banner saying what state the table is in, a podium on the full page,
 * and the table itself.
 *
 * The one screen in the product read for its colour before its numbers, and
 * the one place the standings sub-palette is spent (docs/design/SPEC.md §3.5):
 * medals for places one to three, frost for a frozen table, a tint for your
 * own row, and an identity hue per participant for their initials and score
 * bar. None of it is the only carrier of a fact — the medal holds its number,
 * the freeze has its sentence, your row says "You", the winner says "Winner".
 *
 * Presentational and stateless, so the play tab and the public page share it
 * and each keeps its own fetching.
 */

export type StandingsDictionary = Pick<Dictionary, "leaderboard">;

type Medal = "gold" | "silver" | "bronze";

const MEDALS: Record<number, Medal> = { 1: "gold", 2: "silver", 3: "bronze" };

// Written out whole so Tailwind finds every class in the source.
const MEDAL_CLASSES: Record<
  Medal,
  { disc: string; bar: string; edge: string; step: string }
> = {
  gold: {
    disc: "bg-gold-wash text-gold ring-gold/40",
    bar: "bg-gold",
    edge: "border-l-gold",
    step: "bg-gold-wash border-t-gold",
  },
  silver: {
    disc: "bg-silver-wash text-silver ring-silver/40",
    bar: "bg-silver",
    edge: "border-l-silver",
    step: "bg-silver-wash border-t-silver",
  },
  bronze: {
    disc: "bg-bronze-wash text-bronze ring-bronze/40",
    bar: "bg-bronze",
    edge: "border-l-bronze",
    step: "bg-bronze-wash border-t-bronze",
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

function medalOf(row: StandingsRow): Medal | undefined {
  return row.place === null ? undefined : MEDALS[row.place];
}

/** Whether the contest's window has closed, read against the viewer's clock. */
function ended(endsAt: string | undefined): boolean {
  return endsAt !== undefined && Date.parse(endsAt) <= Date.now();
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
  /** The last refresh failed, and what is shown is the previous copy. */
  failed?: boolean;
}) {
  const t = dict.leaderboard;
  const { state, rows } = standings;
  const leader = rows.reduce((max, r) => Math.max(max, r.points), 0);

  return (
    <div className="flex flex-col gap-4">
      <Banner standings={standings} dict={dict} locale={locale} />

      {failed ? <p className="text-small text-warn">{t.failed}</p> : null}

      {state === "not_started" ? null : rows.length === 0 ? (
        <p className="text-body text-ink-2">{t.empty}</p>
      ) : (
        <>
          {variant === "page" && standings.scoring === "points" ? (
            <Podium rows={rows} dict={dict} />
          ) : null}
          <div className="overflow-x-auto">
            <table className="w-full border-collapse">
              <caption className="sr-only">{t.heading}</caption>
              <thead>
                <tr className="border-b border-line-2 font-mono text-label text-ink-3 uppercase">
                  <th
                    scope="col"
                    className="w-16 py-2 pr-2 pl-3 text-left font-normal"
                  >
                    {t.columns.place}
                  </th>
                  <th scope="col" className="px-2 py-2 text-left font-normal">
                    {t.columns.participant}
                  </th>
                  <th scope="col" className="px-2 py-2 text-right font-normal">
                    {t.columns.points}
                  </th>
                  {variant === "page" ? (
                    <>
                      <th
                        scope="col"
                        className="px-2 py-2 text-right font-normal max-narrow:hidden"
                      >
                        {t.columns.solved}
                      </th>
                      <th
                        scope="col"
                        className="py-2 pr-3 pl-2 text-right font-normal max-narrow:hidden"
                      >
                        {t.columns.last}
                      </th>
                    </>
                  ) : null}
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
}: {
  row: StandingsRow;
  leader: number;
  dict: StandingsDictionary;
  locale: string;
  variant: "panel" | "page";
}) {
  const t = dict.leaderboard;
  const medal = medalOf(row);
  const hue = identityHue(row.label);
  const share = leader > 0 ? Math.round((row.points / leader) * 100) : 0;

  return (
    <tr
      data-you={row.isYou ? "true" : undefined}
      className={cn("border-b border-line", row.isYou && "bg-you-wash")}
    >
      <td
        className={cn(
          "border-l-3 py-2.5 pr-2 pl-3 align-middle",
          medal ? MEDAL_CLASSES[medal].edge : "border-l-transparent",
        )}
      >
        <span data-testid="place" data-medal={medal} className="inline-flex">
          {row.place === null ? (
            <>
              <span aria-hidden className="font-mono text-data text-ink-3">
                —
              </span>
              <span className="sr-only">{t.unplaced}</span>
            </>
          ) : medal ? (
            <span
              className={cn(
                "grid size-7 place-items-center rounded-full font-mono text-label tabular-nums ring-1",
                MEDAL_CLASSES[medal].disc,
              )}
            >
              {row.place}
            </span>
          ) : (
            <span className="grid size-7 place-items-center font-mono text-data text-ink-2 tabular-nums">
              {row.place}
            </span>
          )}
        </span>
      </td>

      <td className="px-2 py-2.5 align-middle">
        <div className="flex min-w-0 items-center gap-2.5">
          <span
            aria-hidden
            data-testid="initials"
            data-hue={row.deleted ? undefined : hue}
            className={cn(
              "grid size-8 shrink-0 place-items-center rounded-full font-mono text-label uppercase",
              row.deleted ? "bg-sunk text-ink-3" : HUE_CLASSES[hue].disc,
            )}
          >
            {row.deleted ? "" : initials(row.label)}
          </span>
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

      <td className="px-2 py-2.5 text-right align-middle">
        <span className="font-mono text-data text-ink tabular-nums">
          {row.points}
        </span>
        <div
          className={cn(
            "mt-1.5 ml-auto h-1 max-w-full rounded-full bg-sunk",
            variant === "page" ? "w-28" : "w-16",
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
    </tr>
  );
}

/**
 * The top three, raised above the table on the full page. Second, first,
 * third, at three heights, so it reads as a podium rather than as a row of
 * three identical cards (SPEC §15). Only rows that actually scored stand on
 * it: a podium of zeroes says nothing the table does not.
 */
function Podium({
  rows,
  dict,
}: {
  rows: StandingsRow[];
  dict: StandingsDictionary;
}) {
  const top = rows
    .filter((r) => r.place !== null && r.place <= 3 && r.points > 0)
    .slice(0, 3);
  if (top.length === 0) return null;

  const order = [top[1], top[0], top[2]];
  const heights = ["h-20", "h-28", "h-14"];

  return (
    <div
      data-testid="podium"
      aria-hidden
      className="grid grid-cols-3 items-end gap-3 pt-2 max-narrow:hidden"
    >
      {order.map((row, i) => {
        if (!row) return <div key={i} />;
        const medal = medalOf(row) ?? "bronze";
        return (
          <div key={i} className="flex min-w-0 flex-col items-center gap-2">
            <span
              className={cn(
                "grid size-12 place-items-center rounded-full font-mono text-h3 ring-2",
                MEDAL_CLASSES[medal].disc,
              )}
            >
              {row.place}
            </span>
            <span className="max-w-full truncate text-body text-ink">
              {row.deleted ? dict.leaderboard.deleted : row.label}
            </span>
            <span className="font-mono text-data text-ink-2 tabular-nums">
              {row.points}
            </span>
            <div
              className={cn(
                "w-full border-t-3",
                heights[i],
                MEDAL_CLASSES[medal].step,
              )}
            />
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
