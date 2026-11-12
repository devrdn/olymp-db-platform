import Link from "next/link";
import type * as React from "react";

import { cn } from "@/lib/utils";

/** A link inside running text or beside a form: underlined, so it reads as one without colour. */
export function InlineLink({ className, ...props }: React.ComponentProps<typeof Link>) {
  return (
    <Link
      className={cn(
        "text-small text-ink-2 underline underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink",
        className,
      )}
      {...props}
    />
  );
}
