"use client";

import { useRouter } from "next/navigation";
import { startTransition } from "react";

import { Button } from "@/components/ui/button";

/**
 * Re-reads the page's server data in place, for a refusal that a fresh copy
 * answers. Client state outside what the server renders survives it, unless
 * `onReload` resets it; that runs in the same transition, so what it resets
 * is shown with the fresh copy rather than before it arrives.
 */
export function ReloadButton({ label, onReload }: { label: string; onReload?: () => void }) {
  const router = useRouter();

  return (
    <Button
      type="button"
      variant="secondary"
      size="sm"
      onClick={() =>
        startTransition(() => {
          router.refresh();
          onReload?.();
        })
      }
      className="self-start"
    >
      {label}
    </Button>
  );
}
