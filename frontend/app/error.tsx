"use client";

import { useEffect } from "react";

import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { AppDictionary } from "@/lib/i18n/client";

/**
 * The catch-all error boundary. Translated, because the root layout puts the
 * dictionary in context, which a client boundary cannot await itself.
 */
export default function RootError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const { dict } = AppDictionary.use();
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
