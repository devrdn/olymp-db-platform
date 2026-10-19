import { ArrowUpRight } from "lucide-react";

import { feedbackHref } from "@/lib/api/settings-terms";
import { cn } from "@/lib/utils";

/**
 * The installation's feedback address as a quiet link, or nothing when none
 * is set. The address passes the API's rule again before it becomes an
 * `href`. It opens in a new tab, so on the play screen it is drawn only
 * where no workspace runs: the workspace reports a hidden page as an absence
 * (use-signals.ts). `noopener` cuts `window.opener`, and `noreferrer` keeps
 * the contest's address out of the form service's logs.
 */
export function FeedbackLink({
  href,
  labels,
  className,
}: {
  href: string;
  labels: { link: string; newTab: string };
  className?: string;
}) {
  const target = feedbackHref(href);
  if (target === null) return null;

  return (
    <a
      href={target}
      target="_blank"
      rel="noopener noreferrer"
      className={cn(
        "inline-flex items-center gap-1 text-ink-2 underline underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink",
        className,
      )}
    >
      {labels.link} <span className="sr-only">{labels.newTab}</span>
      <ArrowUpRight aria-hidden className="size-3.5 shrink-0" />
    </a>
  );
}
