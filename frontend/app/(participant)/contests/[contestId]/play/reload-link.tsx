"use client";

import { useRouter } from "next/navigation";

import { buttonVariants } from "@/components/ui/button";
import type { PlayDictionary } from "./dictionary";
import { cn } from "@/lib/utils";

/**
 * A reload button for the rate-limit refusal, which lifts within the minute
 * (SPEC.md's `blocked` state). Nothing polls: a page reloading itself under
 * a waiting participant is what SPEC.md §6 rules out.
 */
export function ReloadLink({ dict }: { dict: PlayDictionary }) {
  const router = useRouter();

  return (
    <button
      type="button"
      onClick={() => router.refresh()}
      className={cn(buttonVariants({ variant: "secondary", size: "sm" }), "self-start")}
    >
      {dict.participant.play.unavailable.retry}
    </button>
  );
}
