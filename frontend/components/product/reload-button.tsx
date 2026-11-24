"use client";

import { useRouter } from "next/navigation";

import { Button } from "@/components/ui/button";

/**
 * Re-reads the page's server data in place, for a refusal that a fresh copy
 * answers. Client state outside what the server renders survives it.
 */
export function ReloadButton({ label }: { label: string }) {
  const router = useRouter();

  return (
    <Button type="button" variant="secondary" size="sm" onClick={() => router.refresh()} className="self-start">
      {label}
    </Button>
  );
}
