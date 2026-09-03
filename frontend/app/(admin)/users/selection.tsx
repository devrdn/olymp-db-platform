"use client";

import { createContext, useContext, useState, useSyncExternalStore } from "react";

import { Checkbox } from "@/components/ui/checkbox";
import { buttonVariants } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * Which accounts the administrator has picked.
 *
 * An external store read through useSyncExternalStore rather than a context
 * value, because a context re-renders every consumer on every change: with
 * fifty rows on a page, ticking one box would re-render fifty checkboxes and
 * the bar. Here each checkbox subscribes to its own membership and the bar to
 * the count, so a click re-renders one row.
 *
 * The page itself stays a server component. Only the boxes and the bar are
 * client code, which is the whole of what needs state.
 */
class SelectionStore {
  private ids = new Set<string>();
  private listeners = new Set<() => void>();
  // `selected()` used to hand back a snapshot every read is what
  // useSyncExternalStore needs: it calls the getSnapshot function on every
  // render to check for change, and a fresh array each time never compares
  // equal to the last one, so React treats every render as a fresh update
  // and can loop. Caching the array and only rebuilding it on an actual
  // mutation keeps the reference stable between emits.
  private cachedSelected: string[] | null = null;

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  has = (id: string) => this.ids.has(id);
  size = () => this.ids.size;
  selected = () => {
    if (this.cachedSelected === null) this.cachedSelected = [...this.ids];
    return this.cachedSelected;
  };

  toggle = (id: string) => {
    if (!this.ids.delete(id)) this.ids.add(id);
    this.emit();
  };

  replace = (ids: string[]) => {
    this.ids = new Set(ids);
    this.emit();
  };

  private emit() {
    this.cachedSelected = null;
    for (const listener of this.listeners) listener();
  }
}

const EMPTY_SELECTION: readonly string[] = [];

const SelectionContext = createContext<SelectionStore | null>(null);

function useSelectionStore(): SelectionStore {
  const store = useContext(SelectionContext);
  if (!store) {
    throw new Error("useSelectionStore must be used within a SelectionProvider");
  }
  return store;
}

/**
 * Holds the one store instance for the accounts on this page.
 *
 * The instance itself travels through context, which is fine — it never
 * changes identity, so nothing that reads it re-renders when the selection
 * does. What must never live in context is the selected set itself.
 */
export function SelectionProvider({ children }: { children: React.ReactNode }) {
  const [store] = useState(() => new SelectionStore());
  return <SelectionContext.Provider value={store}>{children}</SelectionContext.Provider>;
}

/** The ids currently picked, for the actions the next task adds. */
export function useSelectedIds(): readonly string[] {
  const store = useSelectionStore();
  return useSyncExternalStore(store.subscribe, store.selected, () => EMPTY_SELECTION);
}

/**
 * One row's box. Subscribes only to its own membership, so ticking it
 * re-renders this row and nothing else on the page.
 *
 * `false` as the server snapshot: the server never knows about a selection,
 * so the first client render must agree with the server-rendered markup
 * (unchecked) or React reports a hydration mismatch.
 */
export function RowCheckbox({ id, label }: { id: string; label: string }) {
  const store = useSelectionStore();
  const checked = useSyncExternalStore(store.subscribe, () => store.has(id), () => false);

  return <Checkbox checked={checked} onCheckedChange={() => store.toggle(id)} aria-label={label} />;
}

/**
 * The header box: picks or clears every id on the visible page, and shows
 * the mixed state while only some of them are picked.
 */
export function SelectAllCheckbox({ ids, label }: { ids: string[]; label: string }) {
  const store = useSelectionStore();
  const pickedHere = useSyncExternalStore(
    store.subscribe,
    () => ids.filter((id) => store.has(id)).length,
    () => 0,
  );
  const allPicked = ids.length > 0 && pickedHere === ids.length;
  const somePicked = pickedHere > 0 && !allPicked;

  return (
    <Checkbox
      checked={allPicked}
      indeterminate={somePicked}
      onCheckedChange={() => store.replace(allPicked ? [] : ids)}
      aria-label={label}
    />
  );
}

/**
 * Says how many accounts are picked and offers a way to clear the pick.
 *
 * The actions this bar will eventually offer — block, restore, delete — are
 * next task's work. The marked slot below is where they land; this task adds
 * no button that does nothing.
 */
export function SelectionBar({ dict }: { dict: Dictionary }) {
  const store = useSelectionStore();
  const count = useSyncExternalStore(store.subscribe, () => store.size(), () => 0);
  const t = dict.accounts.selection;

  if (count === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-3 border border-line-2 bg-panel px-4 py-2.5">
      <p role="status" aria-live="polite" className="font-mono text-data text-ink-2">
        {t.count.replace("{n}", String(count))}
      </p>

      {/* The next task wires the bulk actions (block, restore, delete) into
          this slot. Left empty on purpose. */}
      <div data-slot="selection-actions" className="flex flex-1 items-center gap-2" />

      <button
        type="button"
        onClick={() => store.replace([])}
        className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
      >
        {t.clear}
      </button>
    </div>
  );
}
