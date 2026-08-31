import { cn } from "@/lib/utils";

/**
 * A person, in a circle.
 *
 * Initials, never a photograph. SPEC section 10.4 decided that: uploading
 * participants' pictures would create personal data that has to be stored,
 * moderated and deleted on request, and initials answer the same question —
 * which account is this — without any of it.
 *
 * The letters are decoration, not a label. Whatever this sits inside carries
 * the accessible name, so a screen reader hears "Ivan Ivanov" rather than
 * "I I".
 */
export function Avatar({ letters, className }: { letters: string; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "grid size-7 shrink-0 place-items-center rounded-full bg-sunk font-mono text-label text-ink-2 uppercase",
        className,
      )}
    >
      {letters}
    </span>
  );
}
