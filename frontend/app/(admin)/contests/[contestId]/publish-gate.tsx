import { Tag } from "@/components/ui/tag";
import type { CellState, PublishGate } from "@/lib/api/publish-gate";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

const HEAD =
  "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py)";

/**
 * What is missing before publishing. The gate answers 200 with every problem at
 * once, so the whole list is shown beside the disabled button. Contest-level
 * problems are a list; missing translations are a matrix, one row per language.
 */
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
                {/* The server's detail is an English developer aid, shown in
                   monospace so it never reads as translated text; so is the
                   count. */}
                {problem.count > 1 ? (
                  <span className="ml-2 font-mono text-data text-ink-3">×{problem.count}</span>
                ) : problem.detail ? (
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
 * Three states: written, missing in this language, or not written in any (grey,
 * since a fresh draft is not wrong). The mark is visual; the word behind it is
 * for screen readers.
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
