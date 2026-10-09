import { SqlBlock } from "@/components/product/sql-block";
import type { ProfileWorkspace } from "@/lib/api/profile";

import type { ReportDict } from "./report-tabs";

/**
 * The notes and SQL tabs as the contest left them. No edit history: that is a
 * monitoring fact for organisers (SPEC.md §5.2). Notes use the text face inside
 * their `pre`; tabs get the SQL block.
 */
export function MyNotes({ workspace, t }: { workspace: ProfileWorkspace; t: ReportDict }) {
  const notes = workspace.notes.body;
  const tabs = workspace.tabs;

  if (notes === "" && tabs.length === 0) {
    return <p className="max-w-body text-body text-ink-2">{t.notes.empty}</p>;
  }

  return (
    <div className="flex min-w-0 flex-col gap-8">
      <p className="max-w-body text-small text-ink-3">{t.notes.asLeft}</p>

      {/* The page breakpoint, not a container query: nothing here declares
         `@container`, so `@min-[…]` would never match. */}
      <div className="grid min-w-0 grid-cols-2 gap-8 max-narrow:grid-cols-1">
        <section aria-labelledby="report-notes" className="flex min-w-0 flex-col gap-3">
          <h2 id="report-notes" className="font-mono text-label text-ink-3 uppercase">
            {t.notes.notes}
          </h2>
          {notes === "" ? (
            <p className="text-body text-ink-3">{t.notes.emptyNotes}</p>
          ) : (
            <pre className="max-h-96 min-w-0 overflow-auto bg-sunk p-3 font-sans text-body break-words whitespace-pre-wrap text-ink">
              {notes}
            </pre>
          )}
        </section>

        <section aria-labelledby="report-tabs-left" className="flex min-w-0 flex-col gap-3">
          <h2 id="report-tabs-left" className="font-mono text-label text-ink-3 uppercase">
            {t.notes.tabs}
          </h2>
          {tabs.length === 0 ? (
            <p className="text-body text-ink-3">{t.notes.noTabs}</p>
          ) : (
            tabs.map((tab) => (
              <div key={tab.id} className="flex min-w-0 flex-col gap-1.5">
                <h3 className="text-control text-ink">{tab.title}</h3>
                <SqlBlock sql={tab.body} />
              </div>
            ))
          )}
        </section>
      </div>
    </div>
  );
}
