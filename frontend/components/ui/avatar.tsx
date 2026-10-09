import { cn } from "@/lib/utils";

/**
 * Initials in a circle, never a photograph (SPEC §10.4): photos would be
 * personal data to store, moderate and delete. Decorative; the surrounding
 * element carries the accessible name.
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
