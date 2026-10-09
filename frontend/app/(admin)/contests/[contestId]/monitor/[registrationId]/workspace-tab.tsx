"use client";

import { memo, useCallback, useEffect, useId, useMemo, useRef, useState } from "react";

import { buttonVariants } from "@/components/ui/button";
import { ApiError } from "@/lib/api/client";
import { fetchRevision, NOTES_DOCUMENT, type RevisionInfo, type Workspace } from "@/lib/api/monitor";
import { readableBytes } from "@/lib/format/bytes";
import { formatMoment, formatTime } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { SqlBlock } from "@/components/product/sql-block";

import { collapse, lineDiff, type DiffRow } from "./line-diff";

type WorkspaceDict = Dictionary["workspace"]["monitor"]["participant"]["workspace"];

/** Unchanged lines kept around each change. */
const DIFF_CONTEXT = 3;

/** Diff rows rendered at a time; "show more" adds as many again. */
const DIFF_PAGE = 400;

/** Revision bodies cached, so going back and forth reads nothing again. */
const CACHED_BODIES = 32;

type DocumentHistory = {
  document: string;
  /** "Notes", the tab's current title, or its last one. */
  label: string;
  /** Newest first. */
  revisions: RevisionInfo[];
};

/**
 * Revisions per document: notes, open tabs in order, then closed tabs (which
 * keep their history under their last title, SPEC.md §5.1), newest first within
 * each.
 */
export function groupRevisions(workspace: Workspace, t: WorkspaceDict): DocumentHistory[] {
  const byDocument = new Map<string, RevisionInfo[]>();
  for (const revision of workspace.revisions) {
    const list = byDocument.get(revision.document);
    if (list) list.push(revision);
    else byDocument.set(revision.document, [revision]);
  }
  const open = new Map(workspace.tabs.map((tab) => [tab.id, tab]));
  const rank = (document: string) =>
    document === NOTES_DOCUMENT ? -1 : (open.get(document)?.position ?? Number.MAX_SAFE_INTEGER);

  return [...byDocument.entries()]
    .map(([document, revisions]) => ({
      document,
      revisions,
      label:
        document === NOTES_DOCUMENT
          ? t.notes
          : (open.get(document)?.title ?? `${revisions[0].title} (${t.closedTab})`),
    }))
    .sort((a, b) => rank(a.document) - rank(b.document));
}

type Selected =
  | { id: number; state: "loading" }
  | { id: number; state: "failed"; tooOften?: number }
  | {
      id: number;
      state: "ready";
      body: string;
      /** The previous body: null when there is none, undefined when beyond the listed ones. */
      previous: string | null | undefined;
    };

/**
 * The workspace tab (SPEC.md §5.1): notes and SQL tabs as they are now, and each
 * document's revisions with a diff against the previous one. Bodies are read on
 * choosing (the pair together) and cached; the diff is memoised and bounded
 * (`line-diff.ts`).
 */
export function WorkspaceTab({
  contestId,
  registrationId,
  workspace,
  dict,
  locale,
}: {
  contestId: string;
  registrationId: string;
  workspace: Workspace;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor.participant.workspace;
  const history = useMemo(() => groupRevisions(workspace, t), [workspace, t]);
  const [selected, setSelected] = useState<Selected | null>(null);
  const [view, setView] = useState<"changes" | "body">("changes");
  const bodies = useRef(new Map<number, string>());
  const latest = useRef(0);
  const nowId = useId();
  const historyId = useId();

  const body = async (id: number) => {
    const cache = bodies.current;
    const known = cache.get(id);
    if (known !== undefined) return known;
    const revision = await fetchRevision(contestId, registrationId, id, {});
    cache.set(id, revision.body);
    if (cache.size > CACHED_BODIES) cache.delete(cache.keys().next().value as number);
    return revision.body;
  };

  // Each revision's document history and place in it.
  const positions = useMemo(() => {
    const out = new Map<number, { group: DocumentHistory; index: number }>();
    for (const group of history) group.revisions.forEach((revision, index) => out.set(revision.id, { group, index }));
    return out;
  }, [history]);

  const choose = async (id: number) => {
    const position = positions.get(id);
    if (!position) return;
    const revision = position.group.revisions[position.index];
    const before = position.group.revisions[position.index + 1];
    latest.current = revision.id;
    setSelected({ id: revision.id, state: "loading" });
    try {
      const [text, previous] = await Promise.all([
        body(revision.id),
        before ? body(before.id) : Promise.resolve(workspace.truncated ? undefined : null),
      ]);
      if (latest.current !== revision.id) return;
      setSelected({ id: revision.id, state: "ready", body: text, previous });
    } catch (error: unknown) {
      if (latest.current !== revision.id) return;
      const tooOften = error instanceof ApiError && error.status === 429 ? (error.retryAfterSeconds ?? 60) : undefined;
      setSelected({ id: revision.id, state: "failed", tooOften });
    }
  };

  // One stable callback reading `choose` through a ref, so a render does not
  // re-render up to two thousand rows.
  const chooseRef = useRef(choose);
  useEffect(() => {
    chooseRef.current = choose;
  });
  const onChoose = useCallback((id: number) => void chooseRef.current(id), []);

  return (
    <div className="flex min-w-0 flex-col gap-10">
      <section aria-labelledby={nowId} className="flex min-w-0 flex-col gap-4">
        <h3 id={nowId} className="font-mono text-label text-ink-3 uppercase">
          {t.now}
        </h3>
        <div className="grid min-w-0 gap-6 @min-[54rem]:grid-cols-2">
          <div className="flex min-w-0 flex-col gap-2">
            <h4 className="text-h3 text-ink">{t.notes}</h4>
            {workspace.notes.body === "" ? (
              <p className="text-body text-ink-3">{t.emptyNotes}</p>
            ) : (
              <pre className="max-h-96 min-w-0 overflow-auto bg-sunk p-3 font-sans text-body break-words whitespace-pre-wrap text-ink">
                {workspace.notes.body}
              </pre>
            )}
          </div>
          <div className="flex min-w-0 flex-col gap-2">
            <h4 className="text-h3 text-ink">{t.tabs}</h4>
            {workspace.tabs.length === 0 ? (
              <p className="text-body text-ink-3">{t.noTabs}</p>
            ) : (
              workspace.tabs.map((tab) => (
                <div key={tab.id} className="flex min-w-0 flex-col gap-1.5">
                  <h5 className="text-control text-ink">{tab.title}</h5>
                  <SqlBlock sql={tab.body} />
                </div>
              ))
            )}
          </div>
        </div>
      </section>

      <section aria-labelledby={historyId} className="flex min-w-0 flex-col gap-4">
        <h3 id={historyId} className="font-mono text-label text-ink-3 uppercase">
          {t.history}
        </h3>
        {workspace.truncated ? (
          <p className="text-small text-warn">{t.truncated.replace("{n}", String(workspace.revisions.length))}</p>
        ) : null}
        {history.length === 0 ? (
          <p className="text-body text-ink-2">{t.noHistory}</p>
        ) : (
          <div className="grid min-w-0 gap-6 @min-[54rem]:grid-cols-[18rem_minmax(0,1fr)]">
            <div className="flex max-h-[36rem] min-w-0 flex-col gap-5 overflow-y-auto max-narrow:max-h-80">
              {history.map((group) => (
                <RevisionList
                  key={group.document}
                  group={group}
                  selectedId={selected?.id}
                  onChoose={onChoose}
                  t={t}
                  locale={locale}
                />
              ))}
            </div>
            <div className="flex min-w-0 flex-col gap-3">
              {!selected ? (
                <p className="text-body text-ink-2">{t.pick}</p>
              ) : selected.state === "loading" ? (
                <p className="text-body text-ink-3" aria-busy="true">
                  {t.loading}
                </p>
              ) : selected.state === "failed" ? (
                <p role="alert" className="text-body text-bad">
                  {selected.tooOften
                    ? dict.workspace.monitor.problems.tooOften.replace("{seconds}", String(selected.tooOften))
                    : t.failed}
                </p>
              ) : (
                <>
                  <div role="group" className="flex flex-wrap gap-2">
                    {(["changes", "body"] as const).map((option) =>
                      option === "changes" && selected.previous === undefined ? null : (
                        <button
                          key={option}
                          type="button"
                          aria-pressed={view === option}
                          onClick={() => setView(option)}
                          className={cn(buttonVariants({ variant: view === option ? "secondary" : "quiet", size: "sm" }))}
                        >
                          {option === "changes" ? t.changes : t.body}
                        </button>
                      ),
                    )}
                  </div>
                  {view === "changes" && selected.previous !== undefined ? (
                    <RevisionDiff
                      key={selected.id}
                      before={selected.previous ?? ""}
                      after={selected.body}
                      first={selected.previous === null}
                      t={t}
                    />
                  ) : (
                    <section aria-label={t.body} className="min-w-0">
                      <pre className="max-h-[36rem] min-w-0 overflow-auto bg-sunk p-3 font-mono text-data whitespace-pre text-ink">
                        {selected.body}
                      </pre>
                    </section>
                  )}
                </>
              )}
            </div>
          </div>
        )}
      </section>
    </div>
  );
}

/**
 * One document's revisions, newest first. Memoised per row, so choosing renders
 * two rows, not the list.
 */
const RevisionList = memo(function RevisionList({
  group,
  selectedId,
  onChoose,
  t,
  locale,
}: {
  group: DocumentHistory;
  selectedId: number | undefined;
  onChoose: (id: number) => void;
  t: WorkspaceDict;
  locale: string;
}) {
  return (
    <div role="group" aria-label={group.label} className="flex min-w-0 flex-col gap-1">
      <span aria-hidden className="truncate text-control text-ink">
        {group.label}
      </span>
      <ol className="flex flex-col">
        {group.revisions.map((revision) => (
          <RevisionRow
            key={revision.id}
            revision={revision}
            selected={revision.id === selectedId}
            onChoose={onChoose}
            t={t}
            locale={locale}
          />
        ))}
      </ol>
    </div>
  );
});

const RevisionRow = memo(function RevisionRow({
  revision,
  selected,
  onChoose,
  t,
  locale,
}: {
  revision: RevisionInfo;
  selected: boolean;
  onChoose: (id: number) => void;
  t: WorkspaceDict;
  locale: string;
}) {
  return (
    <li>
      <button
        type="button"
        aria-current={selected ? "true" : undefined}
        onClick={() => onChoose(revision.id)}
        title={`${formatMoment(revision.startedAt, { locale })} – ${formatMoment(revision.updatedAt, { locale })}`}
        className={cn(
          "w-full border-l-2 px-2 py-1 text-left font-mono text-label tabular-nums transition-colors duration-(--t-input) ease-standard",
          selected ? "border-ink text-ink" : "border-transparent text-ink-2 hover:border-line-2 hover:text-ink",
        )}
      >
        {t.revision
          .replace("{from}", formatTime(revision.startedAt, { locale }))
          .replace("{to}", formatTime(revision.updatedAt, { locale }))
          .replace("{size}", readableBytes(revision.size))}
      </button>
    </li>
  );
});

/** Each diff line's sign. */
const SIGN = { same: " ", added: "+", removed: "−" } as const;

/** The line diff of two bodies, collapsed around changes and memoised on its inputs. */
const RevisionDiff = memo(function RevisionDiff({
  before,
  after,
  first,
  t,
}: {
  before: string;
  after: string;
  first: boolean;
  t: WorkspaceDict;
}) {
  const diff = useMemo(() => lineDiff(before, after), [before, after]);
  const rows = useMemo<DiffRow[]>(
    () => (first ? diff.lines : collapse(diff.lines, DIFF_CONTEXT)),
    [diff, first],
  );
  const [shown, setShown] = useState(DIFF_PAGE);

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <p className="flex flex-wrap gap-x-3 text-small text-ink-2">
        {first ? (
          <span>{t.first}</span>
        ) : diff.added === 0 && diff.removed === 0 ? (
          <span>{t.identical}</span>
        ) : (
          <span className="font-mono tabular-nums">
            {t.summary.replace("{added}", String(diff.added)).replace("{removed}", String(diff.removed))}
          </span>
        )}
        {diff.exact ? null : <span className="text-warn">{t.notExact}</span>}
      </p>
      <ol
        aria-label={t.changes}
        className="max-h-[36rem] min-w-0 overflow-auto bg-sunk py-2 font-mono text-data text-ink"
      >
        {rows.slice(0, shown).map((row, index) =>
          row.type === "skip" ? (
            <li key={index} data-type="skip" className="px-3 py-1 font-sans text-small text-ink-3">
              {t.unchanged.replace("{n}", String(row.count))}
            </li>
          ) : (
            <li
              key={index}
              data-type={row.type}
              className={cn(
                "flex w-max min-w-full gap-3 px-3 whitespace-pre",
                row.type === "added" && "bg-good-wash",
                row.type === "removed" && "bg-bad-wash",
              )}
            >
              <span aria-hidden className="w-8 shrink-0 text-right text-ink-3 tabular-nums select-none">
                {row.type === "added" ? row.after : row.before}
              </span>
              <span aria-hidden className={cn("shrink-0 select-none", row.type === "same" ? "text-ink-3" : "text-ink")}>
                {SIGN[row.type]}
              </span>
              {row.type === "same" ? null : (
                <span className="sr-only">
                  {row.type === "added"
                    ? t.added.replace("{n}", String(row.after))
                    : t.removed.replace("{n}", String(row.before))}
                </span>
              )}
              <span data-text>{row.text}</span>
            </li>
          ),
        )}
      </ol>
      {rows.length > shown ? (
        <button
          type="button"
          onClick={() => setShown((current) => current + DIFF_PAGE)}
          className={cn(buttonVariants({ variant: "quiet", size: "sm" }), "self-start")}
        >
          {t.showMore}
        </button>
      ) : null}
    </div>
  );
});
