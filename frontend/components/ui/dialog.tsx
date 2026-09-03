"use client";

import * as React from "react";
import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { X } from "lucide-react";

import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/**
 * A modal surface, drawn from this project's own tokens rather than the
 * registry's defaults — the same reasoning as `button.tsx` and `checkbox.tsx`:
 *
 * - Flat, like every other panel in the register (`border border-line-2`, no
 *   ring, no shadow token exists to reach for). The registry's
 *   `ring-1 ring-foreground/10` is a glow, and section 15 has no glows in it.
 * - `rounded-none`, matching the register's own rectangles (`Field`, `Input`,
 *   `Textarea`) rather than the registry's `rounded-xl`. Only controls
 *   (`Button`, `Tag`, `Checkbox`'s absence of one) round at all here.
 * - No `dark:` utility: the theme is a variable swap on `data-theme`, so a
 *   colour written once through a token is already correct in both themes.
 */

/**
 * `dismissible` (default `true`) is the policy this component was missing:
 * with nothing to say otherwise, Escape, an outside click and the corner X
 * always closed the popup, which is safe for a plain confirmation but wrong
 * for a dialog that is mid-request or is holding a result the caller must
 * acknowledge before it can be lost — a bulk password reset's one-time
 * passwords, for one. Pass `dismissible={false}` for that window; the caller
 * decides when, this component only enforces it.
 *
 * Escape and the close button route through `onOpenChange` regardless of
 * `disablePointerDismissal` (that prop only governs an outside press), so
 * both are blocked the same way: the change event fires and this cancels it
 * via `eventDetails.cancel()`, Base UI's own mechanism for refusing a close
 * (see `DialogRoot.ChangeEventDetails`) — nothing here reaches for a
 * hand-rolled keydown listener.
 */
function Dialog({
  dismissible = true,
  onOpenChange,
  ...props
}: DialogPrimitive.Root.Props & { dismissible?: boolean }) {
  return (
    <DialogPrimitive.Root
      data-slot="dialog"
      disablePointerDismissal={!dismissible}
      onOpenChange={(open, eventDetails) => {
        if (!open && !dismissible) {
          eventDetails.cancel();
          return;
        }
        onOpenChange?.(open, eventDetails);
      }}
      {...props}
    />
  );
}

function DialogTrigger({ ...props }: DialogPrimitive.Trigger.Props) {
  return <DialogPrimitive.Trigger data-slot="dialog-trigger" {...props} />;
}

function DialogPortal({ ...props }: DialogPrimitive.Portal.Props) {
  return <DialogPrimitive.Portal data-slot="dialog-portal" {...props} />;
}

function DialogClose({ ...props }: DialogPrimitive.Close.Props) {
  return <DialogPrimitive.Close data-slot="dialog-close" {...props} />;
}

function DialogBackdrop({ className, ...props }: DialogPrimitive.Backdrop.Props) {
  return (
    <DialogPrimitive.Backdrop
      data-slot="dialog-backdrop"
      className={cn(
        "fixed inset-0 z-50 bg-ink/45",
        "transition-opacity duration-(--t-input) ease-standard",
        "data-starting-style:opacity-0 data-ending-style:opacity-0",
        className,
      )}
      {...props}
    />
  );
}

/**
 * The window itself. `closeLabel` names the corner control for assistive
 * tech. `dismissible` (default `true`) disables that control — rather than
 * hiding it, so the layout does not jump — when the caller's `Dialog` is
 * refusing Escape and outside clicks too; the two props are meant to travel
 * together.
 */
function DialogContent({
  className,
  children,
  closeLabel,
  dismissible = true,
  ...props
}: DialogPrimitive.Popup.Props & { closeLabel: string; dismissible?: boolean }) {
  return (
    <DialogPortal>
      <DialogBackdrop />
      <DialogPrimitive.Popup
        data-slot="dialog-content"
        className={cn(
          "fixed top-1/2 left-1/2 z-50 flex w-full max-w-md -translate-x-1/2 -translate-y-1/2 flex-col gap-5",
          "border border-line-2 bg-panel p-6 outline-none",
          "transition-[opacity,transform] duration-(--t-input) ease-standard",
          "data-starting-style:scale-95 data-starting-style:opacity-0",
          "data-ending-style:scale-95 data-ending-style:opacity-0",
          className,
        )}
        {...props}
      >
        {children}
        {/* Styled with `buttonVariants` directly rather than through a
            `Button` — `DialogPrimitive.Close` already renders its own
            native button, the same way the selection bar's own "Clear
            selection" control does below its own `buttonVariants` call. */}
        <DialogPrimitive.Close
          data-slot="dialog-close"
          disabled={!dismissible}
          className={cn(buttonVariants({ variant: "quiet", size: "icon" }), "absolute top-3 right-3")}
        >
          <X />
          <span className="sr-only">{closeLabel}</span>
        </DialogPrimitive.Close>
      </DialogPrimitive.Popup>
    </DialogPortal>
  );
}

function DialogHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="dialog-header"
      className={cn("flex flex-col gap-1.5 pr-8", className)}
      {...props}
    />
  );
}

function DialogFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="dialog-footer"
      className={cn("flex flex-wrap items-center justify-end gap-3", className)}
      {...props}
    />
  );
}

function DialogTitle({ className, ...props }: DialogPrimitive.Title.Props) {
  return (
    <DialogPrimitive.Title
      data-slot="dialog-title"
      className={cn("text-h3 text-ink", className)}
      {...props}
    />
  );
}

function DialogDescription({ className, ...props }: DialogPrimitive.Description.Props) {
  return (
    <DialogPrimitive.Description
      data-slot="dialog-description"
      className={cn("text-small text-ink-2", className)}
      {...props}
    />
  );
}

export {
  Dialog,
  DialogBackdrop,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPortal,
  DialogTitle,
  DialogTrigger,
};
