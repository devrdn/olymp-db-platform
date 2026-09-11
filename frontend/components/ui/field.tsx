import * as React from "react";

import { Label } from "@/components/ui/label";
import { Tooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

/**
 * An explanation behind a "?" beside the label, or none. Both halves or
 * neither: a "?" with no name is a button a screen reader announces as
 * nothing, so the name travels with the text rather than being optional.
 */
type FieldHelp =
  | { help?: undefined; helpLabel?: undefined }
  | {
      /** Why the field is what it is — read once, then in the way. */
      help: React.ReactNode;
      /** The "?" button's accessible name (`chrome.helpLabel`). */
      helpLabel: string;
    };

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
 * The hint is for what somebody must know *before* they get it wrong — a
 * format, a limit, what an empty field means — and it stays on screen. Why a
 * field exists or what it does goes in `help` instead: a "?" beside the label
 * that opens a `Tooltip`. That split is the client's, made deliberately (the
 * image rule was once moved above the upload so a 5 MB phone photo stops
 * being refused after the fact; behind a hover it would be refused after the
 * fact again). The "?" sits beside the `<label>`, never inside it, so the
 * control's name stays exactly the label.
 *
 * The explanation also describes the control itself, not only the "?". A
 * screen-reader user moving between form fields — NVDA's F key, VoiceOver's
 * form-control rotor — lands on the control and never on the button beside
 * its label. Before explanations moved behind a question mark that user heard
 * the whole explanation on reaching the field; describing only the "?" would
 * have taken it away from exactly the people who could not see the clutter
 * the question mark exists to remove. The cost is that somebody who Tabs
 * through both hears it twice, which is the lesser loss. The bubble stays in
 * the DOM while closed (see `Tooltip`), so the description resolves whether
 * or not it is open.
 *
 * A failure that belongs to the form rather than to one field is passed as
 * `invalid` plus `describedBy`: the message is written once, elsewhere, and
 * every field it concerns points at it.
 */
export function Field({
  id,
  label,
  hint,
  help,
  helpLabel,
  error,
  invalid,
  describedBy,
  className,
  children,
}: {
  id: string;
  label: React.ReactNode;
  /** A short rule that stays visible under the control. */
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
} & FieldHelp) {
  const messageId = `${id}-message`;
  const helpId = `${id}-help`;
  const message = error ?? hint;
  const describedByIds = [message ? messageId : null, help && helpLabel ? helpId : null, describedBy]
    .filter(Boolean)
    .join(" ");

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
      {help && helpLabel ? (
        <div className="flex items-center gap-1.5">
          <Label htmlFor={id}>{label}</Label>
          <Tooltip id={helpId} label={helpLabel}>
            {help}
          </Tooltip>
        </div>
      ) : (
        <Label htmlFor={id}>{label}</Label>
      )}
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
