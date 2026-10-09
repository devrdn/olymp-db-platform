"use client";

import * as React from "react";
import { Combobox as ComboboxPrimitive } from "@base-ui/react/combobox";

import { Tooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

/** One item the picker offers. */
export type ComboboxOption<T> = {
  /** Stable identity across re-fetches. */
  key: string;
  value: T;
  /** Shown in the input once chosen, and matched inside the popup. */
  label: string;
  /** A second line under the label, e.g. a login under a name. */
  description?: string;
};

/**
 * A searchable picker. `filter={null}` disables client-side re-filtering:
 * callers fetch an already-matched set, and re-matching against the displayed
 * label would drop rows the server matched on other fields (login, email).
 * Selection is compared by `key`, since re-fetched items are new objects.
 */
export function Combobox<T>({
  id,
  label,
  items,
  inputValue,
  onInputValueChange,
  value,
  onValueChange,
  placeholder,
  emptyMessage,
  statusMessage,
  disabled,
  className,
  describedBy,
  help,
  helpLabel,
}: {
  id: string;
  label: string;
  items: ComboboxOption<T>[];
  inputValue: string;
  onInputValueChange: (value: string) => void;
  value: ComboboxOption<T> | null;
  onValueChange: (value: ComboboxOption<T> | null) => void;
  placeholder?: string;
  emptyMessage: string;
  /** Announced politely to screen readers: "Searching…", a count, or nothing. */
  statusMessage?: string;
  disabled?: boolean;
  className?: string;
  /**
   * Id of a paragraph elsewhere (a hint, a "Selected: …" line) wired to the
   * input's `aria-describedby`.
   */
  describedBy?: string;
  help?: string;
  /** Required with `help`. */
  helpLabel?: string;
}) {
  return (
    <ComboboxPrimitive.Root
      items={items}
      inputValue={inputValue}
      onInputValueChange={(next) => onInputValueChange(next)}
      value={value}
      onValueChange={(next) => onValueChange(next)}
      itemToStringLabel={(item: ComboboxOption<T>) => item.label}
      isItemEqualToValue={(item: ComboboxOption<T>, current: ComboboxOption<T> | null) =>
        item.key === current?.key
      }
      filter={null}
      disabled={disabled}
      autoHighlight
    >
      {/* A native label: `Combobox.Label` only associates with
         `Combobox.Trigger`, which this typeahead does not use. */}
      {/* Beside the label, never inside, so the input's name is the label alone. */}
      <div className="flex items-center gap-1.5">
        <label htmlFor={id} className="font-mono text-label text-ink-3 uppercase">
          {label}
        </label>
        {help && helpLabel ? <Tooltip label={helpLabel}>{help}</Tooltip> : null}
      </div>

      <ComboboxPrimitive.Input
        id={id}
        placeholder={placeholder}
        aria-describedby={describedBy}
        className={cn(
          "h-(--control-h) w-full min-w-0 border border-edge bg-transparent px-3",
          "text-control text-ink placeholder:text-ink-3",
          "transition-colors duration-(--t-input) ease-standard",
          "hover:border-ink-2 focus-visible:border-ink",
          className,
        )}
      />

      <ComboboxPrimitive.Portal>
        <ComboboxPrimitive.Positioner sideOffset={4} className="z-50 outline-none">
          <ComboboxPrimitive.Popup
            className={cn(
              "max-h-72 w-(--anchor-width) overflow-y-auto",
              "border border-line-2 bg-panel py-1 outline-none",
            )}
          >
            {/* Requires `items` on the root and must stay mounted, so its text
               is toggled instead. */}
            <ComboboxPrimitive.Empty className="px-3 py-2 text-small text-ink-3">
              {emptyMessage}
            </ComboboxPrimitive.Empty>

            <ComboboxPrimitive.List>
              {(item: ComboboxOption<T>) => (
                <ComboboxPrimitive.Item
                  key={item.key}
                  value={item}
                  className={cn(
                    "flex cursor-pointer flex-col px-3 py-2 text-row text-ink",
                    "data-highlighted:bg-sunk data-selected:font-medium",
                  )}
                >
                  <span>{item.label}</span>
                  {item.description ? (
                    <span className="font-mono text-data text-ink-3">{item.description}</span>
                  ) : null}
                </ComboboxPrimitive.Item>
              )}
            </ComboboxPrimitive.List>
          </ComboboxPrimitive.Popup>
        </ComboboxPrimitive.Positioner>
      </ComboboxPrimitive.Portal>

      {/* Visually hidden, not unmounted: the announcement needs the element
         present when its text changes. */}
      <ComboboxPrimitive.Status className="sr-only" aria-live="polite">
        {statusMessage}
      </ComboboxPrimitive.Status>
    </ComboboxPrimitive.Root>
  );
}
