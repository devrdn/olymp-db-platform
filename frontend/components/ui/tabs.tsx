"use client";

import * as React from "react";
import { Tabs as TabsPrimitive } from "@base-ui/react/tabs";

import { cn } from "@/lib/utils";

/**
 * Tabs that switch what is visible without ever remounting it.
 *
 * Built for the play workspace (Task 3 of the game-ui plan): a half-typed
 * query, a scroll position, an already-rendered result table all have to
 * survive a switch between "Result" and "Query log", or between "Story" and
 * "Questions" — so `TabsContent` defaults `keepMounted` to `true`, the
 * opposite of Base UI's own default. Every panel using this stays in the
 * React tree the whole time; only the DOM's native `hidden` attribute
 * changes, which is what keeps a hidden panel out of layout and costing no
 * reflow — Base UI's own `TabsPanel` sets exactly that attribute rather than
 * `display:none` in a stylesheet or `visibility:hidden`, and marks the panel
 * `inert` while hidden so neither focus nor a screen reader ever lands on it.
 *
 * Manual activation (Base UI's own default): a tab becomes active on click or
 * on Enter/Space once it has focus, not on arrow-key focus alone. Switching
 * costs nothing here — every panel is already rendered — but the manual
 * pattern is still the one WAI-ARIA recommends for a plain tab list, and nothing
 * about this screen calls for departing from it.
 *
 * Styling follows this project's own flat, ruled direction (`dialog.tsx`'s own
 * doc explains the reasoning once): no glow, no shadow, `rounded-none`, an
 * underline for the active tab rather than a filled pill.
 */
function Tabs({ className, ...props }: TabsPrimitive.Root.Props) {
  return <TabsPrimitive.Root data-slot="tabs" className={cn("flex min-h-0 flex-col", className)} {...props} />;
}

function TabsList({ className, ...props }: TabsPrimitive.List.Props) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      className={cn("flex shrink-0 items-stretch border-b border-line", className)}
      {...props}
    />
  );
}

function TabsTrigger({ className, ...props }: TabsPrimitive.Tab.Props) {
  return (
    <TabsPrimitive.Tab
      data-slot="tabs-trigger"
      className={cn(
        "-mb-px border-b-2 border-transparent px-3 py-2 font-mono text-label text-ink-3 uppercase",
        "transition-colors duration-(--t-input) ease-standard",
        "hover:text-ink",
        "data-[selected]:border-ink data-[selected]:text-ink",
        "outline-none focus-visible:ring-2 focus-visible:ring-accent-ink focus-visible:ring-offset-2 focus-visible:ring-offset-bg",
        className,
      )}
      {...props}
    />
  );
}

function TabsContent({ className, keepMounted = true, ...props }: TabsPrimitive.Panel.Props) {
  return (
    <TabsPrimitive.Panel
      data-slot="tabs-content"
      keepMounted={keepMounted}
      className={cn("min-h-0 flex-1 [&[hidden]]:hidden", className)}
      {...props}
    />
  );
}

export { Tabs, TabsList, TabsTrigger, TabsContent };
