import Link from "next/link";

import { Button } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import { cn } from "@/lib/utils";

/**
 * The specification's states the constructor reaches. The type is the contract:
 * `empty` has no reset and `empty-filtered` requires one; `error-recoverable`
 * requires a retry and `error-terminal` refuses one (a 403 or 404 does not
 * change on retry). `blocked` is the publication gate: disabled with the reason
 * and the missing work named.
 *
 * `loading` is absent because each container draws its own `Skeleton`.
 */
export type ViewState =
  | {
      kind: "empty";
      title: string;
      body: string;
      /** The next step, where one exists. */
      action?: { label: string; href: string };
    }
  | {
      kind: "empty-filtered";
      title: string;
      body: string;
      reset: { label: string; href: string };
    }
  | {
      kind: "error-recoverable";
      title: string;
      body: string;
      retry: { label: string; onRetry: () => void };
      reference?: { label: string; value: string };
    }
  | {
      kind: "error-terminal";
      title: string;
      body: string;
      exit?: { label: string; href: string };
    }
  | {
      kind: "blocked";
      title: string;
      body: string;
      /** What is missing, or when the block lifts. */
      detail?: string;
      badge?: string;
    };

/** Both non-primary exits: an outlined pill that navigates. */
const EXIT =
  "mt-1 inline-flex h-(--control-h) items-center rounded-full border border-edge px-4 text-control text-ink transition-colors duration-(--t-input) ease-standard hover:border-ink";

export function StateView({ state, className }: { state: ViewState; className?: string }) {
  return (
    <div className={cn("flex flex-col items-start gap-3 py-14", className)}>
      <div className="flex flex-wrap items-center gap-2.5">
        <h2 className="text-h3 text-ink">{state.title}</h2>
        {state.kind === "blocked" && state.badge ? <Tag tone="warn">{state.badge}</Tag> : null}
      </div>

      <p className="max-w-body text-body text-ink-2">{state.body}</p>

      {state.kind === "empty-filtered" ? (
        <Link href={state.reset.href} className={EXIT}>
          {state.reset.label}
        </Link>
      ) : null}

      {state.kind === "empty" && state.action ? (
        <Link href={state.action.href} className={EXIT}>
          {state.action.label}
        </Link>
      ) : null}

      {state.kind === "error-recoverable" ? (
        <>
          <Button className="mt-1" onClick={state.retry.onRetry}>
            {state.retry.label}
          </Button>
          {state.reference ? (
            <p className="pt-1 font-mono text-data text-ink-3">
              {state.reference.label}: {state.reference.value}
            </p>
          ) : null}
        </>
      ) : null}

      {state.kind === "error-terminal" && state.exit ? (
        <Link href={state.exit.href} className={EXIT}>
          {state.exit.label}
        </Link>
      ) : null}

      {state.kind === "blocked" && state.detail ? (
        <p className="pt-1 font-mono text-data text-ink-2">{state.detail}</p>
      ) : null}
    </div>
  );
}
