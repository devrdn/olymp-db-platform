"use client";

import { useEffect } from "react";

import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { SessionDictionary } from "@/lib/i18n/client";

/**
 * The profile could not be built. This group decides from `/auth/me` whether
 * someone is signed in: "not signed in" redirects to the form, "the server did
 * not answer" lands here with a retry. Conflating them made a restarted API
 * look like an expired session.
 */
export default function ProfileError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const { dict } = SessionDictionary.use();
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
