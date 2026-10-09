"use client";

import { memo, useEffect, useMemo, useRef, useState } from "react";

import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { tokenizeSql, type SqlTokenKind } from "./sql-tokens";

/** The editor's colour tokens (code-editor-core.ts). */
const TONE: Record<Exclude<SqlTokenKind, "plain">, string> = {
  keyword: "text-accent",
  function: "text-(--sql-function)",
  string: "text-(--sql-string)",
  number: "text-(--sql-number)",
  comment: "text-ink-3 italic",
};

/**
 * A read-only highlighted statement that scrolls inside its own box both ways.
 * Memoised with its tokens, so an unrelated re-render does not re-tokenise.
 */
export const SqlBlock = memo(function SqlBlock({ sql, label }: { sql: string; label?: string }) {
  const tokens = useMemo(() => tokenizeSql(sql), [sql]);
  return (
    <pre
      aria-label={label}
      className="max-h-96 min-w-0 overflow-auto bg-sunk p-3 font-mono text-data whitespace-pre text-ink"
    >
      <code>
        {tokens.map((token, index) =>
          token.kind === "plain" ? (
            token.text
          ) : (
            <span key={index} data-token={token.kind} className={TONE[token.kind]}>
              {token.text}
            </span>
          ),
        )}
      </code>
    </pre>
  );
});

/** How long "Copied" stays. */
const COPIED_MS = 2_000;

/**
 * Copies text and reports the result in a live region that is always rendered,
 * so the change is announced.
 */
export function CopyButton({
  text,
  labels,
}: {
  text: string;
  labels: { copy: string; copied: string; copyFailed: string };
}) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  const copy = async () => {
    let next: "copied" | "failed" = "copied";
    try {
      if (!navigator.clipboard) throw new Error("no clipboard");
      await navigator.clipboard.writeText(text);
    } catch {
      next = "failed";
    }
    setState(next);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setState("idle"), COPIED_MS);
  };

  return (
    <span className="inline-flex items-center gap-2">
      <button type="button" onClick={copy} className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}>
        {labels.copy}
      </button>
      <span role="status" className={cn("text-small", state === "failed" ? "text-bad" : "text-ink-3")}>
        {state === "copied" ? labels.copied : state === "failed" ? labels.copyFailed : ""}
      </span>
    </span>
  );
}
