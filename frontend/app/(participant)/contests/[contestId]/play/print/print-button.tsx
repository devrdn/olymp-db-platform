"use client";

import { Button } from "@/components/ui/button";

/**
 * The one control on the print-ready view, and the one thing on it that must
 * never itself appear on a printed page.
 *
 * There is no PDF library behind it — `docs/ARCHITECTURE.md`'s own plan calls
 * for `window.print()` and a print stylesheet, and nothing else: an
 * on-premise deployment for a university gets a "Save as PDF" for free from
 * every browser's own print dialog, at the cost of zero kilobytes in a
 * participant's bundle. A client component is unavoidable here specifically
 * because `window.print()` is a browser action with no URL of its own — the
 * one place on this screen where "a download is a link" (SPEC.md §11.1)
 * genuinely does not apply, because nothing is being downloaded: the browser
 * is producing the artefact from markup already on the page, the same page
 * this button sits on.
 *
 * `print:hidden` is load-bearing, not decoration: without it, the button that
 * exists solely to *open* the print dialog would be sitting on every page the
 * dialog then renders.
 */
export function PrintButton({ label }: { label: string }) {
  return (
    <Button type="button" onClick={() => window.print()} className="print:hidden">
      {label}
    </Button>
  );
}
