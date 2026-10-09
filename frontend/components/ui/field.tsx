import * as React from "react";

import { Label } from "@/components/ui/label";
import { Tooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

/** Both or neither: a "?" without a name is announced as nothing. */
type FieldHelp =
  | { help?: undefined; helpLabel?: undefined }
  | {
      help: React.ReactNode;
      helpLabel: string;
    };

/**
 * Label, control and message as one block, wired from one place: the control
 * gets the label's `for`, `aria-describedby` and `aria-invalid`.
 *
 * A hint and an error share one slot, so an error moves nothing. The hint is
 * what someone must know before they get it wrong and stays visible; `help`
 * opens a `Tooltip` beside (never inside) the `<label>`.
 *
 * The explanation also describes the control, because screen-reader users
 * moving between form fields land on the control, not on the "?".
 *
 * A form-level failure is passed as `invalid` plus `describedBy`, pointing at a
 * message written once elsewhere.
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
  hint?: React.ReactNode;
  error?: React.ReactNode;
  /** Marks the control invalid without a message of its own, for a form-level failure. */
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
   * Only keys with a value go into the clone: `cloneElement` writes an explicit
   * `undefined`, which would erase an `aria-invalid` the caller set.
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
