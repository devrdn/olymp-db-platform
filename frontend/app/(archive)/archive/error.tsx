"use client";

import { useEffect } from "react";

import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { ArchiveDictionary } from "@/lib/i18n/client";

/**
 * The recoverable-error state: a network or 5xx failure. A refusal the page
 * can name (the rate limit) is answered in the page, and an absent archive is
 * a not-found, so neither reaches here.
 */
export default function ArchiveError({
  error,
  retry,
}: {
  error: Error & { digest?: string };
  // Re-fetches the children; `reset` would only re-render the failed result.
  retry: () => void;
}) {
  const { dict } = ArchiveDictionary.use();
  const t = dict.archive.failed;

  useEffect(() => {
    console.error(error);
  }, [error]);

  return (
    <Band fill className="py-12">
      <div className="border-t border-line">
        <StateView
          state={{
            kind: "error-recoverable",
            title: t.title,
            body: t.body,
            retry: { label: t.retry, onRetry: retry },
            reference: error.digest ? { label: t.reference, value: error.digest } : undefined,
          }}
        />
      </div>
    </Band>
  );
}
