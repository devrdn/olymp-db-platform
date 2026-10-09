"use client";

import { useEffect } from "react";

import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { ParticipantDictionary } from "@/lib/i18n/client";

/**
 * The recoverable-error state. Next reduces a Server Component error to a
 * digest, so the API's code is unknown here; only network and 5xx failures
 * reach this boundary (the page redirects the rest), so a retry is offered,
 * with the digest to match a server log line.
 */
export default function OpenContestsError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const { dict } = ParticipantDictionary.use();
  // The shared retry and reference wording; only the two sentences that
  // name what failed belong to this screen.
  const t = dict.participant.failed;
  const mine = dict.participant.open.failed;

  useEffect(() => {
    console.error(error);
  }, [error]);

  return (
    <Band fill className="py-12">
      <div className="border-t border-line">
        <StateView
          state={{
            kind: "error-recoverable",
            title: mine.title,
            body: mine.body,
            retry: { label: t.retry, onRetry: reset },
            reference: error.digest ? { label: t.reference, value: error.digest } : undefined,
          }}
        />
      </div>
    </Band>
  );
}
