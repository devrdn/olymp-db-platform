"use client";

import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * Tabs that switch what is visible without ever remounting it.
 *
 * Built for the play workspace (Task 3 of the game-ui plan): a half-typed
 * query, a scroll position, an already-rendered result table all have to
 * survive a switch between "Result" and "Query log", or between "Story" and
 * "Questions" — so every panel stays in the React tree the whole time; only
 * the DOM's native `hidden` attribute changes, which is what keeps a hidden
 * panel out of layout and costing no reflow.
 *
 * Hand-rolled rather than Base UI's `Tabs` (finding 6): that composite pulls
 * its whole roving-focus engine plus floating-ui utilities — about 25 KB raw,
 * 13.4 KB gzipped on this route — to drive four buttons and a `hidden`
 * attribute, on the one screen hundreds of students load at the same minute.
 * What is here is the same handful of DOM facts the WAI-ARIA tabs pattern
 * asks for, written directly: `role="tablist"`/`"tab"`/`"tabpanel"`,
 * `aria-selected`, `aria-controls`/`aria-labelledby` pairing a tab to its
 * panel, and a roving `tabIndex` with arrow-key movement between tabs
 * (Home/End included). Activation is manual — a tab becomes selected on
 * click, or on Enter/Space once arrow keys have moved focus to it, not on
 * arrow-key focus alone — which is the pattern WAI-ARIA recommends for a
 * plain tab list and the one the previous Base UI usage already followed.
 *
 * This also fixes finding 1 by construction: `TabsContent` can render `flex
 * flex-col` (a flex *container*, not just a flex *item*), so a child that
 * asks for `flex-1` — `ResultPanel`'s own root, for one — actually gets a
 * height to fill rather than sizing to its content inside a block box. That
 * is opt-in via the `fill` prop (default `true`, matching every panel that
 * needed it first) rather than forced on every panel unconditionally
 * (finding 3 of the follow-up review): a panel with no child that must fill
 * the height — the story, or the questions list, both of which scroll the
 * whole tab rather than a bounded inner child — gets a plain block box, the
 * layout ordinary flowed content (paragraph margins included) already
 * expects. Every panel is rendered unconditionally, always in the DOM, with
 * `hidden` toggled on the ones not selected — `[hidden]` still needs
 * `!important` here (`[&[hidden]]:hidden`) because the `flex` utility, when
 * `fill` applies it, would otherwise win the display property.
 *
 * Styling follows this project's own flat, ruled direction (`dialog.tsx`'s own
 * doc explains the reasoning once): no glow, no shadow, `rounded-none`, an
 * underline for the active tab rather than a filled pill.
 */

type TabsContextValue = {
  value: string;
  setValue: (value: string) => void;
  baseId: string;
};

const TabsContext = React.createContext<TabsContextValue | null>(null);

function useTabsContext(component: string): TabsContextValue {
  const ctx = React.useContext(TabsContext);
  if (!ctx) throw new Error(`<${component}> must be rendered inside <Tabs>`);
  return ctx;
}

function Tabs({
  value,
  defaultValue,
  onValueChange,
  className,
  children,
  ...props
}: {
  /** Controlled selection. Omit and use `defaultValue` for an uncontrolled tab group. */
  value?: string;
  defaultValue?: string;
  onValueChange?: (value: string) => void;
  className?: string;
  children?: React.ReactNode;
} & Omit<React.ComponentPropsWithoutRef<"div">, "onChange" | "defaultValue" | "value">) {
  const baseId = React.useId();
  const [internalValue, setInternalValue] = React.useState(defaultValue ?? "");
  const isControlled = value !== undefined;
  const current = isControlled ? value : internalValue;

  const setValue = React.useCallback(
    (next: string) => {
      if (!isControlled) setInternalValue(next);
      onValueChange?.(next);
    },
    [isControlled, onValueChange],
  );

  const ctx = React.useMemo<TabsContextValue>(() => ({ value: current, setValue, baseId }), [current, setValue, baseId]);

  return (
    <TabsContext.Provider value={ctx}>
      <div data-slot="tabs" className={cn("flex min-h-0 flex-col", className)} {...props}>
        {children}
      </div>
    </TabsContext.Provider>
  );
}

/**
 * The roving-tabindex owner: arrow keys move focus among this list's own
 * `[role="tab"]` children (wrapping at the ends), Home/End jump to the first
 * or last. Moving focus this way never selects a tab by itself — only a
 * click, or Enter/Space on the focused tab, does (manual activation, this
 * component's own doc).
 */
function TabsList({ className, children, onKeyDown, ...props }: React.ComponentPropsWithoutRef<"div">) {
  const listRef = React.useRef<HTMLDivElement>(null);

  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    onKeyDown?.(event);
    if (event.defaultPrevented) return;
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;

    const tabs = Array.from(listRef.current?.querySelectorAll<HTMLElement>('[role="tab"]') ?? []);
    if (tabs.length === 0) return;
    const currentIndex = tabs.indexOf(document.activeElement as HTMLElement);

    let nextIndex = currentIndex;
    if (event.key === "ArrowRight") nextIndex = currentIndex < 0 ? 0 : (currentIndex + 1) % tabs.length;
    else if (event.key === "ArrowLeft") nextIndex = currentIndex < 0 ? 0 : (currentIndex - 1 + tabs.length) % tabs.length;
    else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = tabs.length - 1;

    event.preventDefault();
    tabs[nextIndex]?.focus();
  };

  return (
    <div
      ref={listRef}
      data-slot="tabs-list"
      role="tablist"
      onKeyDown={handleKeyDown}
      className={cn("flex shrink-0 items-stretch border-b border-line", className)}
      {...props}
    >
      {children}
    </div>
  );
}

function TabsTrigger({
  value,
  className,
  children,
  onClick,
  ...props
}: { value: string } & Omit<React.ComponentPropsWithoutRef<"button">, "value">) {
  const { value: selected, setValue, baseId } = useTabsContext("TabsTrigger");
  const isSelected = selected === value;

  return (
    <button
      type="button"
      data-slot="tabs-trigger"
      role="tab"
      id={`${baseId}-tab-${value}`}
      aria-controls={`${baseId}-panel-${value}`}
      aria-selected={isSelected}
      // Roving tabindex: only the selected tab sits in the regular tab
      // order, matching the WAI-ARIA tabs pattern — a screen reader user
      // tabs once into the list, then arrow-keys between tabs.
      tabIndex={isSelected ? 0 : -1}
      onClick={(event) => {
        onClick?.(event);
        setValue(value);
      }}
      className={cn(
        "-mb-px border-b-2 border-transparent px-3 py-2 font-mono text-label text-ink-3 uppercase",
        "transition-colors duration-(--t-input) ease-standard",
        "hover:text-ink",
        isSelected && "border-ink text-ink",
        "outline-none focus-visible:ring-2 focus-visible:ring-accent-ink focus-visible:ring-offset-2 focus-visible:ring-offset-bg",
        className,
      )}
      {...props}
    >
      {children}
    </button>
  );
}

function TabsContent({
  value,
  className,
  children,
  /**
   * Whether this panel is a flex *container* its own children can fill —
   * `ResultPanel` and the query log both have a `flex-1` root that needs a
   * bounded height to scroll inside (finding 1). Default `true` for that
   * reason, but a panel that only holds ordinarily-flowing content — prose,
   * a form — should pass `fill={false}`: forcing `flex-col` on it buys
   * nothing (nothing inside asks to fill the height) and turns every direct
   * child into a flex item, which is not the layout plain block content was
   * written for (finding 3).
   */
  fill = true,
  ...props
}: { value: string; fill?: boolean } & Omit<React.ComponentPropsWithoutRef<"div">, "value">) {
  const { value: selected, baseId } = useTabsContext("TabsContent");
  const isSelected = selected === value;

  return (
    <div
      data-slot="tabs-content"
      role="tabpanel"
      id={`${baseId}-panel-${value}`}
      aria-labelledby={`${baseId}-tab-${value}`}
      // Present the whole time (never conditionally rendered) so state
      // inside a hidden panel — a scroll position, an in-progress answer —
      // survives the switch; `hidden` is what takes it out of layout and
      // out of the accessibility tree without unmounting it. `inert` on top
      // of that keeps focus and a screen reader's virtual cursor from ever
      // landing inside a panel that is not showing.
      hidden={!isSelected}
      inert={!isSelected}
      tabIndex={0}
      className={cn("min-h-0 [&[hidden]]:hidden", fill && "flex flex-1 flex-col", className)}
      {...props}
    >
      {children}
    </div>
  );
}

export { Tabs, TabsList, TabsTrigger, TabsContent };
