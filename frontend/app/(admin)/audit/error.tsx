"use client";

import { useEffect } from "react";

import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { AdminDictionary } from "@/lib/i18n/client";

/**
 * Recoverable error with a retry. Next reduces a Server Component error to a
 * digest, so the API code is unknown here; what reaches this boundary is
 * network or 5xx, worth retrying. The digest ties the screen to a server log
 * line.
 */
export default function ContestsError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const { dict } = AdminDictionary.use();
  const t = dict.audit.failed;

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
            retry: { label: t.retry, onRetry: reset },
            reference: error.digest
              ? { label: t.reference, value: error.digest }
              : undefined,
          }}
        />
      </div>
    </Band>
  );
}
