"use client";

import { useEffect } from "react";

import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { useDictionary } from "@/lib/i18n/client";

/**
 * The recoverable-error state: a cause and a way forward.
 *
 * Next strips a Server Component error down to a digest before it reaches the
 * browser, so the API's machine code is not available here and this screen
 * does not pretend to know it. What it can honestly offer is a retry, which is
 * the whole point of the state: the failures that reach this boundary are
 * network and 5xx, and both are worth trying again — the ones that are not
 * (a dead session, a password still to be changed) were turned into redirects
 * by the page before they ever got here. The digest is shown because it is the
 * one thing tying this screen to a line in the server's log.
 */
export default function MyContestsError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const { dict } = useDictionary();
  const t = dict.participant.failed;

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
            reference: error.digest ? { label: t.reference, value: error.digest } : undefined,
          }}
        />
      </div>
    </Band>
  );
}
