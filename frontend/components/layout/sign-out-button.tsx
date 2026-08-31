import { LogOut } from "lucide-react";

import { signOutAction } from "./session-actions";

/**
 * The way out, in the bar.
 *
 * A form submitting to a Server Action rather than a link, for the reason
 * every sign-out should be: ending a session changes state, and a GET that
 * changes state is one a prefetch, a crawler or an image tag can trigger. As a
 * POST it also inherits Next's Origin check, so another site cannot sign our
 * visitors out.
 *
 * Two shapes for two places. On the profile screen it is a labelled control
 * among other account actions, which is where it belongs. In the bar it is
 * icon-only, and appears on exactly one screen: the forced password change,
 * whose account cannot open a profile at all because the API refuses it every
 * endpoint but three.
 */
export function SignOutButton({ label, withLabel }: { label: string; withLabel?: boolean }) {
  return (
    <form action={signOutAction} className="flex">
      <button
        type="submit"
        title={withLabel ? undefined : label}
        aria-label={withLabel ? undefined : label}
        className={
          withLabel
            ? "flex items-center gap-2 rounded-full text-control text-ink-2 transition-colors duration-(--t-input) ease-standard hover:text-bad"
            : "grid size-7 place-items-center rounded-full text-ink-3 transition-colors duration-(--t-input) ease-standard hover:bg-sunk hover:text-ink"
        }
      >
        <LogOut className="size-4 shrink-0" strokeWidth={1.75} aria-hidden />
        {withLabel ? label : null}
      </button>
    </form>
  );
}
