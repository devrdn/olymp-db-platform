"use client";

import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * Tabs that switch what is visible without remounting it: every panel stays in
 * the tree and only `hidden` changes, so a half-typed query, a scroll position
 * and a rendered result survive a switch.
 *
 * Hand-rolled rather than Base UI's `Tabs`, which costs about 13 KB gzipped on
 * the play route. Implements the WAI-ARIA tabs pattern with manual activation:
 * arrow keys and Home/End move focus, click or Enter/Space selects.
 *
 * `[&[hidden]]:hidden` is needed because the `flex` utility would otherwise win
 * the display property.
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
  /** Controlled selection; omit and use `defaultValue` for uncontrolled. */
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
 * Owns the roving tabindex: arrows move focus (wrapping), Home/End jump to the
 * ends. Moving focus never selects.
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
      // Only the selected tab is in the tab order (WAI-ARIA tabs pattern).
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
        "outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-bg",
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
   * Makes the panel a flex column, so a `flex-1` child (`ResultPanel`, the
   * query log) gets a bounded height to scroll in. Pass `false` for plain
   * flowing content such as prose or a form.
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
      // Always rendered so state inside survives a switch; `inert` keeps focus
      // and screen readers out while hidden.
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
