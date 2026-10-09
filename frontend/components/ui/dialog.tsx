"use client";

import * as React from "react";
import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { X } from "lucide-react";

import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/**
 * A modal surface on the project's tokens: flat, no ring or shadow,
 * `rounded-none`, and no `dark:` utilities since the theme is a variable swap.
 */

/**
 * The dialog root. `dismissible={false}` blocks Escape, an outside press and
 * the corner X, for a dialog mid-request or holding a result that must be
 * acknowledged (one-time passwords). Escape and the close button go through
 * `onOpenChange` regardless of `disablePointerDismissal`, so the close is
 * refused there with `eventDetails.cancel()`.
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
 * The dialog's window. `closeLabel` names the corner control.
 * `dismissible={false}` disables it rather than hiding it, so the layout does
 * not jump; pass it together with the `Dialog` prop.
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
        {/* `DialogPrimitive.Close` renders its own button, so it takes
           `buttonVariants` directly. */}
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
