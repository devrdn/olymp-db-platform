import Link from "next/link";

import { Button } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import { cn } from "@/lib/utils";

/**
 * The states of the specification that the constructor actually reaches.
 *
 * The type is the contract. `empty` has no reset control, because there is no
 * filter to clear and offering one would be a lie; `empty-filtered` cannot be
 * rendered without one, because without it the screen is a dead end. Those two
 * are the pair that gets confused most often, and here the confusion does not
 * compile. The same holds for the error pair: `error-recoverable` requires a
 * retry, `error-terminal` refuses one — a 403 on somebody else's contest and a
 * 404 on a question that belongs to another one (architecture section 7.3) do
 * not become true on a second attempt, and a button claiming otherwise wastes
 * the author's time.
 *
 * `blocked` is the publication gate's state: the button is disabled with the
 * reason named and the missing work listed, not greyed out in silence.
 *
 * Two of the nine are deliberately absent. `loading` is drawn from `Skeleton`
 * by each container, because its appearance is a property of the content it
 * stands in for. `degraded` and `provisioning` arrive with the game loop —
 * truncated result sets and a game database still being built are steps 4 and
 * 5 of the implementation order, and a state nothing renders is a state that
 * rots.
 */
export type ViewState =
  | {
      kind: "empty";
      title: string;
      body: string;
      /**
       * Where to go instead. Optional, because not every emptiness has a next
       * step — but where one exists, principle 4 says to name it: an accurate
       * screen that leaves the reader with nothing to do is only half a state.
       */
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
      /** What is missing, or when the block lifts. A block without either is a wall. */
      detail?: string;
      badge?: string;
    };

/** Both non-primary ways out share a shape: an outlined pill that navigates. */
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
