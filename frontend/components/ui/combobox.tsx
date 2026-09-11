"use client";

import * as React from "react";
import { Combobox as ComboboxPrimitive } from "@base-ui/react/combobox";

import { Tooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

/**
 * A searchable picker, drawn from this project's own tokens rather than the
 * registry's — the same reasoning as `dialog.tsx` and `checkbox.tsx`.
 *
 * The list is always exactly what the caller passed in `items`: `filter={null}`
 * turns off the primitive's own client-side re-filtering, because the callers
 * this project has all fetch an already-matched result set from the server —
 * re-filtering a substring match against a label built from more fields than
 * the label shows (a login and an email, folded into one displayed name)
 * would silently drop matches the server was right to return.
 *
 * Selection is compared by `key`, not by reference: a picker whose items come
 * back from a fresh request every keystroke never has the same object twice,
 * even for the option already chosen.
 */
export type ComboboxOption<T> = {
  /** Stable identity, compared across re-fetches — never the object itself. */
  key: string;
  value: T;
  /** What the input shows once this option is chosen, and what it is matched
   * by inside the popup. */
  label: string;
  /** A second line under the label — a login under a name, for one. */
  description?: string;
};

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
  /** Announced politely to screen readers as it changes — "Searching…", a
   * count, or nothing once the answer is on screen. */
  statusMessage?: string;
  disabled?: boolean;
  className?: string;
  /** id of a paragraph elsewhere on the page — a hint, a "Selected: …" line
   * — that describes this control. Wired to the input's own
   * `aria-describedby` rather than left for the caller to attach by hand,
   * which is how a describing paragraph ends up in the DOM with nothing
   * pointing at it. */
  describedBy?: string;
  /** An explanation behind a "?" beside the label, as `Field` has one. */
  help?: string;
  /** The "?" button's accessible name (`chrome.helpLabel`); required with `help`. */
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
      {/* A native label, not `Combobox.Label`: the primitive's own label only
          associates with `Combobox.Trigger`, which this picker does not use
          — it is a typeahead built on `Combobox.Input` directly, and the
          primitive's own development warning says a native label (or
          `Field.Label`) is what labels that form control. */}
      {/* The "?" beside the label, never inside it, so the input's name
          stays the label alone — the same rule `Field` follows. */}
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
            {/* Requires `items` on the root, and must stay mounted — see the
                primitive's own doc comment — so its text is toggled rather
                than the element itself. */}
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

      {/* Visually hidden, not unmounted: the primitive announces a change in
          its text to screen readers, which needs the element to still be
          there when the change happens. */}
      <ComboboxPrimitive.Status className="sr-only" aria-live="polite">
        {statusMessage}
      </ComboboxPrimitive.Status>
    </ComboboxPrimitive.Root>
  );
}
