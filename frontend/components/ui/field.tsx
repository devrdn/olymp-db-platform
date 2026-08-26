import * as React from "react";

import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

/**
 * Label, control and message as one block.
 *
 * The wiring is the reason this exists rather than three elements written by
 * hand: the control gets the label's `for`, the message's `aria-describedby`
 * and `aria-invalid` from one place, so a field cannot ship with the label
 * pointing at nothing — which is the defect this component is here to make
 * impossible.
 *
 * A hint and an error share one slot. The hint holds the space, so turning an
 * error on moves nothing under the cursor; a field with neither reserves no
 * space at all.
 *
 * A failure that belongs to the form rather than to one field is passed as
 * `invalid` plus `describedBy`: the message is written once, elsewhere, and
 * every field it concerns points at it.
 */
export function Field({
  id,
  label,
  hint,
  error,
  invalid,
  describedBy,
  className,
  children,
}: {
  id: string;
  label: React.ReactNode;
  hint?: React.ReactNode;
  error?: React.ReactNode;
  /**
   * Marks the control invalid without giving it a message of its own — for a
   * failure that belongs to the form rather than to one field.
   */
  invalid?: boolean;
  /** Id of a message written elsewhere that also describes this control. */
  describedBy?: string;
  className?: string;
  children: React.ReactElement<React.ComponentProps<"input">>;
}) {
  const messageId = `${id}-message`;
  const message = error ?? hint;
  const describedByIds = [message ? messageId : null, describedBy].filter(Boolean).join(" ");

  /**
   * Only keys that carry a value go into the clone.
   *
   * `cloneElement` treats an `undefined` in its config as a value to write, not
   * as "leave this alone", so spelling out `"aria-invalid": undefined` erases
   * an `aria-invalid` the caller put on the control itself. That is how the
   * sign-in form ended up announcing nothing on a rejected password: the
   * attribute was set, and this component took it back off.
   */
  const wiring: React.ComponentProps<"input"> = { id };
  if (error || invalid) wiring["aria-invalid"] = true;
  if (describedByIds) wiring["aria-describedby"] = describedByIds;

  const control = React.cloneElement(children, wiring);

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <Label htmlFor={id}>{label}</Label>
      {control}
      {message ? (
        <p
          id={messageId}
          role={error ? "alert" : undefined}
          className={cn("text-small", error ? "text-bad" : "text-ink-3")}
        >
          {message}
        </p>
      ) : null}
    </div>
  );
}
