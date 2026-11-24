"use client";

import { useState, type FormEvent } from "react";

import { Textarea } from "@/components/ui/textarea";

/** A form's required reason, as `useRequiredReason` holds it. */
export type RequiredReason = {
  /** `reason_required` while the reason is missing, for the form's outcome. */
  refusal: "reason_required" | undefined;
  /** The form's `onSubmit`: an empty or whitespace-only reason stops the submission. */
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  /** The field's `onChange`: the refusal goes as soon as the reason is written. */
  onChange: (value: string) => void;
};

/**
 * The reason a form refuses to send without, read from its `reason` field.
 * Before hydration React replays a submission without `onSubmit`; the server
 * refuses that one with the same code. `required` false asks for nothing.
 */
export function useRequiredReason(required = true): RequiredReason {
  const [missing, setMissing] = useState(false);

  return {
    refusal: missing ? "reason_required" : undefined,
    onSubmit: (event) => {
      if (!required) return;
      const empty = String(new FormData(event.currentTarget).get("reason") ?? "").trim() === "";
      if (empty) event.preventDefault();
      setMissing(empty);
    },
    onChange: (value) => {
      if (missing && value.trim() !== "") setMissing(false);
    },
  };
}

/** The `reason` field, invalid while `reason` refuses. */
export function ReasonField({ id, label, reason }: { id: string; label: string; reason: RequiredReason }) {
  return (
    <div className="flex flex-col gap-2">
      <label htmlFor={id} className="font-mono text-label text-ink-3 uppercase">
        {label}
      </label>
      {/* `required` for ARIA only: the form is `noValidate`, so the browser's
         unlocalised bubble never fires. */}
      <Textarea
        id={id}
        name="reason"
        required
        aria-invalid={reason.refusal !== undefined || undefined}
        className="min-h-24 font-sans text-body"
        onChange={(event) => reason.onChange(event.currentTarget.value)}
      />
    </div>
  );
}
