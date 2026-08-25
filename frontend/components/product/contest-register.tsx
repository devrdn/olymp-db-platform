import Link from "next/link";

import type { ContestStatus, ContestSummary } from "@/lib/api/contests";
import { formatMoment } from "@/lib/format/datetime";

/**
 * The contest listing, as a register rather than a wall of cards.
 *
 * A row carries more than a tile at the same height and can be compared down a
 * column, which a grid of tiles cannot. It is a real `<table>`: the data is
 * tabular, so the semantics come for free and a screen reader announces which
 * column a cell belongs to.
 */

const STATUS_LABELS: Record<ContestStatus, string> = {
  draft: "черновик",
  published: "опубликована",
  running: "идёт",
  finished: "завершена",
  archived: "в архиве",
};

const HEAD_CELL =
  "px-3 py-2 font-mono text-[0.625rem] font-medium tracking-[0.11em] text-ink-3 uppercase";

/**
 * "Nothing here" and "nothing matched" are different states and get different
 * screens (spec section 7). The first has no filter to clear, so offering the
 * control would be a lie; the second is useless without it.
 */
function EmptyRegister({ filtered, resetHref }: { filtered?: boolean; resetHref?: string }) {
  return (
    <div className="flex flex-col items-start gap-2 border-t border-line px-1 py-12">
      <p className="font-medium">{filtered ? "Ничего не нашлось" : "Олимпиад пока нет"}</p>
      <p className="max-w-[48ch] text-sm text-ink-2">
        {filtered
          ? "Ни одна олимпиада не подходит под выбранные условия."
          : "Создайте первую — она появится здесь и станет доступна участникам после публикации."}
      </p>
      {filtered && resetHref ? (
        <Link
          href={resetHref}
          className="mt-2 rounded-full border border-edge px-4 py-1.5 text-sm font-medium transition-colors duration-150 hover:border-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
        >
          Сбросить фильтры
        </Link>
      ) : null}
    </div>
  );
}

export function ContestRegister({
  contests,
  total,
  filtered,
  resetHref,
}: {
  contests: ContestSummary[];
  total: number;
  filtered?: boolean;
  resetHref?: string;
}) {
  return (
    <section aria-labelledby="register-heading">
      <div className="flex items-baseline justify-between gap-4 px-1 pb-3">
        <h2 id="register-heading" className="text-lg font-medium tracking-tight">
          Олимпиады
        </h2>
        <span className="font-mono text-xs text-ink-3 tabular-nums">{total} в реестре</span>
      </div>

      {contests.length === 0 ? (
        <EmptyRegister filtered={filtered} resetHref={resetHref} />
      ) : (
        /* Wide content scrolls inside its own container, so the page body never
           scrolls sideways on a narrow screen. */
        <div className="overflow-x-auto border-t border-line">
          <table className="w-full min-w-[46rem] border-collapse text-left">
            <thead>
              <tr className="border-b border-line-2">
                <th scope="col" className={`${HEAD_CELL} w-10 text-right`}>
                  №
                </th>
                <th scope="col" className={HEAD_CELL}>
                  Олимпиада
                </th>
                <th scope="col" className={HEAD_CELL}>
                  Состояние
                </th>
                <th scope="col" className={HEAD_CELL}>
                  Запись
                </th>
                <th scope="col" className={HEAD_CELL}>
                  Начало
                </th>
              </tr>
            </thead>
            <tbody>
              {contests.map((contest, index) => (
                <tr key={contest.id} className="border-b border-line align-baseline">
                  <td className="px-3 py-3 text-right font-mono text-[0.6875rem] text-ink-3 tabular-nums">
                    {String(index + 1).padStart(2, "0")}
                  </td>
                  <td className="px-3 py-3">
                    <span className="block font-medium tracking-tight">{contest.title}</span>
                    {contest.description ? (
                      <span className="mt-1 block max-w-[52ch] text-sm text-ink-2">
                        {contest.description}
                      </span>
                    ) : null}
                  </td>
                  <td className="px-3 py-3 text-sm">{STATUS_LABELS[contest.status]}</td>
                  <td className="px-3 py-3 text-sm">
                    {contest.enrollment === "open" ? "открытая" : "по приглашению"}
                  </td>
                  <td className="px-3 py-3 font-mono text-[0.6875rem] text-ink-2 tabular-nums">
                    {contest.startsAt ? formatMoment(contest.startsAt) : "не назначено"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
