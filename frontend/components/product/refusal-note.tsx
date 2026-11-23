import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";
import { cn } from "@/lib/utils";

/**
 * A refusal a page shows in place of what it read, such as the reader's own
 * rate limit: its sentence from the dictionary, announced without
 * interrupting.
 */
export function RefusalNote({
  code,
  errors,
  className,
}: {
  code: string;
  errors: Pick<Dictionary["errors"], "fallback">;
  className?: string;
}) {
  return (
    <p role="status" className={cn("text-body text-warn", className)}>
      {messageForCode(code, errors)}
    </p>
  );
}
