"use client";

import { useId } from "react";

import { cn } from "@/lib/utils";
import type { PlayDictionary } from "./dictionary";
import type { PhonePane } from "./panel-toggles";

/**
 * The switch a phone gets in place of the pane grid: one of the editor, the
 * result, the schema and the questions on screen at a time (SPEC.md §5).
 *
 * A radio group rather than tabs: the panes are plain regions from `narrow`
 * up, where a `tabpanel` role would describe a switch that is not there.
 * Native radios bring the arrow keys and the checked state with them.
 */
export function PaneSwitch({
  value,
  onChange,
  schema,
  marks,
  switchRef,
  dict,
}: {
  value: PhonePane;
  onChange: (pane: PhonePane) => void;
  /** False in a contest that hides its schema: no such part. */
  schema: boolean;
  /**
   * What waits unseen behind a part, in words: a dot for the eye and a
   * description for a screen reader, the name staying fixed (see `Toggle` in
   * `panel-toggles.tsx`).
   */
  marks: Partial<Record<PhonePane, string>>;
  switchRef?: React.Ref<HTMLDivElement>;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace.phone;
  const name = useId();
  const notePrefix = useId();
  const parts: readonly PhonePane[] = schema ? ["editor", "result", "schema", "side"] : ["editor", "result", "side"];

  return (
    <div
      ref={switchRef}
      role="radiogroup"
      aria-label={t.label}
      // Under the app bar's `h-12` and its 1px rule, and below its `z-20` so
      // the bar's menu opens over the switch.
      className="sticky top-[calc(3rem+1px)] z-10 flex shrink-0 border-b border-line bg-bg narrow:hidden"
    >
      {parts.map((pane) => (
        // `flex-auto`: each part starts from its own word and they share what is
        // left, so the longest label is the last to be cut on a small phone.
        <label key={pane} className="relative flex min-h-11 min-w-0 flex-auto cursor-pointer items-stretch">
          <input
            type="radio"
            name={name}
            value={pane}
            checked={value === pane}
            onChange={() => onChange(pane)}
            // A checked radio fires no change; a tap on it still asks for its
            // pane, which may be showing something other than its name says.
            onClick={() => {
              if (value === pane) onChange(pane);
            }}
            aria-describedby={marks[pane] ? `${notePrefix}-${pane}` : undefined}
            className="peer sr-only"
          />
          <span
            className={cn(
              "flex min-w-0 flex-1 items-center justify-center gap-1.5 border-b-2 border-transparent px-1 font-mono text-label text-ink-3 uppercase",
              "transition-colors duration-(--t-input) ease-standard",
              "peer-checked:border-ink peer-checked:text-ink",
              "peer-focus-visible:outline-2 peer-focus-visible:-outline-offset-2 peer-focus-visible:outline-accent",
            )}
          >
            <span className="truncate">{t[pane]}</span>
            {marks[pane] ? (
              <span aria-hidden data-testid="pane-switch-marker" className="size-1.5 shrink-0 rounded-full bg-accent" />
            ) : null}
          </span>
        </label>
      ))}
      {/* Outside the labels, or they would be read as part of a part's name. */}
      {parts.map((pane) =>
        marks[pane] ? (
          <span key={pane} id={`${notePrefix}-${pane}`} className="sr-only">
            {marks[pane]}
          </span>
        ) : null,
      )}
    </div>
  );
}
