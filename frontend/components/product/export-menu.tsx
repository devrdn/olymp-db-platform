import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/**
 * One export format. `href` is an API path on this origin, never a blob built
 * in the page: the server decides what leaves under that endpoint's permission,
 * and a page-built blob could hold only what the page was given. `format`
 * ("CSV") is not translated.
 */
export type ExportFormat = {
  format: string;
  href: string;
  /** Accessible name: what is downloaded, not merely the format. */
  label: string;
};

/**
 * Download links (ARCHITECTURE.md §9.1). Plain anchors, so keyboard, middle
 * click, "save as" and no-JavaScript all work and the browser reports progress.
 * The server's `Content-Disposition` decides the file name. Each surface offers
 * one format today.
 */
export function ExportMenu({
  heading,
  formats,
  className,
}: {
  /** Names the group for a screen reader; the visible label is on the links. */
  heading: string;
  formats: ExportFormat[];
  className?: string;
}) {
  if (formats.length === 0) return null;

  return (
    <nav aria-label={heading} className={cn("flex items-center gap-2", className)}>
      {formats.map((format) => (
        <a
          key={format.href}
          href={format.href}
          download
          aria-label={format.label}
          className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
        >
          {format.format}
        </a>
      ))}
    </nav>
  );
}
