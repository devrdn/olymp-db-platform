"use client";

import { useEffect } from "react";

import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { useDictionary } from "@/lib/i18n/client";

/**
 * When the account screen could not be built.
 *
 * It exists because this group is the one that decides, from `/auth/me`,
 * whether somebody is signed in — and that question now has three answers, not
 * two. "You are not signed in" is a redirect to the form; "the server did not
 * answer" is this screen, with a retry that can actually work.
 *
 * Collapsing the second into the first is what made a restarted API look like
 * an expired session, and sent people back to a form they had just filled in.
 */
export default function ProfileError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const { dict } = useDictionary();
  const t = dict.profile.failed;

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
            retry: { label: dict.participant.failed.retry, onRetry: reset },
            reference: error.digest
              ? { label: dict.participant.failed.reference, value: error.digest }
              : undefined,
          }}
        />
      </div>
    </Band>
  );
}
