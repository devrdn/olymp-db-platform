"use client";

import { useEffect } from "react";

import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { useDictionary } from "@/lib/i18n/client";

/**
 * The last boundary before the framework's own error page.
 *
 * A route with a boundary of its own keeps it; this one catches everything
 * else, so a failure outside `/contests` no longer lands on an untranslated
 * default. It speaks all three languages because the dictionary is in context
 * from the root layout, which a client boundary cannot await for itself.
 */
export default function RootError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const { dict } = useDictionary();
  const t = dict.screens.failure;

  useEffect(() => {
    console.error(error);
  }, [error]);

  return (
    <Band fill>
      <StateView
        state={{
          kind: "error-recoverable",
          title: t.title,
          body: t.body,
          retry: { label: t.retry, onRetry: reset },
          reference: error.digest ? { label: t.reference, value: error.digest } : undefined,
        }}
      />
    </Band>
  );
}
