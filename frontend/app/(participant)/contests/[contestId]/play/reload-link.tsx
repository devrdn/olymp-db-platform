"use client";

import { useRouter } from "next/navigation";

import { buttonVariants } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The way back from a refusal that lifts by itself.
 *
 * Only the rate limit gets this. SPEC.md's state list keeps "retry" for the
 * network and 5xx, and a `blocked` state is supposed to name "the reason and
 * the moment it lifts" instead — but here the moment is "within the minute"
 * and the way to find out is to ask again, so a button is the honest shape.
 * Nothing polls: this screen belongs to somebody who is waiting, and a page
 * that reloads itself under them is the thing SPEC.md §6 rules out.
 */
export function ReloadLink({ dict }: { dict: Dictionary }) {
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
