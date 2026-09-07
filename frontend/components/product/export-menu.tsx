import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/**
 * One format a screen can hand its data over in.
 *
 * `href` is an API path on this origin — `/api/v1/...` — never a blob built in
 * the page. The distinction is the whole design: what leaves the server is
 * decided by the server, under the permission that endpoint sits behind, and
 * the browser only asks for it. A client-side assembly could only ever contain
 * what the page had already been given, which for the two exports that exist
 * is either nothing (the answer key is never sent to a page) or a fraction of
 * it (the log panel holds one page of rows, the file holds the session).
 *
 * `format` is the short name shown on the control — "CSV", "JSON". It is not
 * translated: a file format is a proper noun in every language this interface
 * speaks, and `.csv` is what the downloaded file is called anyway.
 */
export type ExportFormat = {
  format: string;
  href: string;
  /** The accessible name, which says what is being downloaded, not merely how. */
  label: string;
};

/**
 * The download control of a screen that has data worth taking away
 * (docs/ARCHITECTURE.md §9.1, docs/design/SPEC.md §11).
 *
 * Plain anchors, one per format. Not a dropdown, and not a button that fetches
 * and builds a blob: a download is exactly what a link is for, so this works
 * with the keyboard, with a middle click, with "save as", and with no
 * JavaScript running at all — and the browser's own download indicator is what
 * reports progress on a file the server streams. The `download` attribute only
 * suggests a name; the server's own `Content-Disposition` is what actually
 * decides it, which is why the two exports both set one.
 *
 * Called a menu because it is meant to grow one: §9.1 promises NDJSON and XLSX
 * beside CSV on the administrator's journal panel. Today each surface offers
 * one format, so each renders one link — a list of one, rather than a
 * disclosure widget hiding a single item.
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
