import { LogOut } from "lucide-react";

import { signOutAction } from "./session-actions";

/**
 * A form posting to a Server Action, not a link: a state-changing GET can be
 * fired by a prefetch, crawler or image tag, and a POST gets Next's Origin
 * check. Labelled on the profile; icon-only in the bar, where it appears only
 * on the forced password change.
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
