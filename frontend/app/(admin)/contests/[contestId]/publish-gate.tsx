import { Tag } from "@/components/ui/tag";
import type { CellState, PublishGate } from "@/lib/api/publish-gate";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * What is still missing before this contest can be published.
 *
 * The gate answers 200 even when publishing is impossible — being asked what
 * is left is not a failure — and it returns *every* problem at once. An author
 * fixing one refusal per round trip would make as many round trips as they
 * have untranslated questions, so the whole list is shown at once and the
 * publish button is disabled with this beside it.
 *
 * Two shapes, because there are two kinds of problem. A missing schedule or a
 * question count that does not match the format belongs to the contest and has
 * no cell to sit in; a missing translation belongs to one language, and those
 * are a matrix — one row per declared language, so an author can see that
 * Romanian is three questions behind English without counting.
 */
const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py)";

export function PublishGateReport({
  gate,
  ready,
  dict,
}: {
  gate: PublishGate;
  ready: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.gate;

  if (ready) {
    return (
      <div className="flex items-center gap-2.5">
        <Tag tone="good">{t.readyBadge}</Tag>
        <p className="text-body text-ink-2">{t.ready}</p>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-2.5">
        <Tag tone="warn">{t.blockedBadge}</Tag>
        <p className="text-body text-ink-2">{t.blocked}</p>
      </div>

      {gate.global.length > 0 ? (
        <ul className="flex flex-col gap-2">
          {gate.global.map((problem) => (
            <li key={problem.code} className="flex gap-2.5 text-small text-ink-2">
              <span aria-hidden className="mt-2 size-1 shrink-0 rounded-full bg-warn" />
              <span>
                {(t.problems as Record<string, string>)[problem.code] ?? problem.code}
                {/* The server's detail is a developer's aid in English. It is
                    shown in the monospace register, where the interface's own
                    prose ends and raw data begins, so it never reads as a
                    translated sentence. */}
                {problem.detail ? (
                  <span className="ml-2 font-mono text-data text-ink-3">{problem.detail}</span>
                ) : null}
              </span>
            </li>
          ))}
        </ul>
      ) : null}

      {gate.byLanguage.length > 0 ? (
        <div className="overflow-x-auto">
          <table className="w-full min-w-md border-collapse text-left">
            <thead>
              <tr>
                <th scope="col" className={cn(HEAD, "w-24")}>
                  {t.columns.language}
                </th>
                <th scope="col" className={HEAD}>
                  {t.columns.title}
                </th>
                <th scope="col" className={HEAD}>
                  {t.columns.story}
                </th>
                <th scope="col" className={HEAD}>
                  {t.columns.questions}
                </th>
              </tr>
            </thead>
            <tbody>
              {gate.byLanguage.map((row) => (
                <tr key={row.lang}>
                  <td className={cn(CELL, "font-mono text-data text-ink uppercase")}>{row.lang}</td>
                  <td className={CELL}>
                    <Mark state={row.title} dict={dict} />
                  </td>
                  <td className={CELL}>
                    <Mark state={row.story} dict={dict} />
                  </td>
                  <td className={cn(CELL, "font-mono text-data")}>
                    {row.questionsNotStarted ? (
                      <Mark state="not-started" dict={dict} />
                    ) : row.questionsMissing === 0 ? (
                      <Mark state="ok" dict={dict} />
                    ) : (
                      <span className="text-warn">
                        {t.questionsMissing.replace("{n}", String(row.questionsMissing))}
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  );
}

/**
 * A cell's verdict, in three states rather than two.
 *
 * "Not written in this language" and "not written at all" look the same to an
 * author if both are a dash, and only the first is theirs to fix here — the
 * second is one piece of work for every language at once. The dot says the
 * work has not begun; it is grey, not amber, because nothing is wrong with a
 * draft that has no story yet.
 *
 * The mark is a character and the word behind it is for a screen reader: a
 * tick carries nothing read aloud, and against a language name it is the
 * entire content of the cell.
 */
function Mark({ state, dict }: { state: CellState; dict: Dictionary }) {
  const t = dict.workspace.gate;

  const face = { ok: "✓", missing: "—", "not-started": "·" }[state];
  const label = { ok: t.done, missing: t.missing, "not-started": t.notStarted }[state];
  const tone = { ok: "text-good", missing: "text-warn", "not-started": "text-ink-3" }[state];

  return (
    <span className={cn("font-mono text-data", tone)}>
      <span aria-hidden>{face}</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}
