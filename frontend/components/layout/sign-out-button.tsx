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
 * Icon-only, like its two neighbours in the bar, with the label carried by
 * `aria-label` and `title`. It sits last of the three because it is the one
 * with a consequence, and because a control that ends the session should not
 * be the one the thumb reaches first.
 */
export function SignOutButton({ label }: { label: string }) {
  return (
    <form action={signOutAction} className="flex">
      <button
        type="submit"
        title={label}
        aria-label={label}
        className="grid size-7 place-items-center rounded-full text-ink-3 transition-colors duration-(--t-input) ease-standard hover:bg-sunk hover:text-ink"
      >
        <LogOut className="size-4" strokeWidth={1.75} aria-hidden />
      </button>
    </form>
  );
}
