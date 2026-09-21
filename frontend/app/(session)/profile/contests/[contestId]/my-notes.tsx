import { SqlBlock } from "@/components/product/sql-block";
import type { ProfileWorkspace } from "@/lib/api/profile";

import type { ReportDict } from "./report-tabs";

/**
 * The notes and the SQL tabs as the contest left them.
 *
 * No history of the edits, and no way to ask for one. The record of how a
 * document changed is a monitoring fact about how somebody worked — the
 * organiser's tool — and handing it back to its author would be a second
 * feature wearing this one's clothes (design §2.2). What a participant came
 * for is what they wrote.
 *
 * Notes are prose and are set in the text face even inside the `pre` that
 * keeps their line breaks; a tab is SQL and gets the highlighted block the
 * rest of the product reads statements in.
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

      {/* Two columns down to the page's one breakpoint, one below it. The
          page's own breakpoint rather than a container query: nothing in
          this screen declares `@container`, and a `@min-[…]` class without
          one is a rule that never matches at any width. */}
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
