"use client";

import { useState, useTransition } from "react";
import { useRouter } from "next/navigation";

import { buttonVariants } from "@/components/ui/button";
import type { Game } from "@/lib/api/game";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { requestGameBuildAction } from "./actions";

/**
 * Asking for the game to be built again, and the one sentence that explains
 * why anybody would.
 *
 * The table builder's rows are stored after the build that would have loaded
 * them and cannot be stored before it — a row may only be typed into a table
 * the saved definition already names, and saving the definition is what
 * starts the build. So a builder game is normally built empty first and
 * filled afterwards, and this is how the filling reaches a database.
 *
 * Nothing is rendered once the contest is running. The API refuses the
 * request there (raising the version drops and remakes every participant's
 * copy), and a button that exists to be refused is worse than no button.
 */
export function GameBuild({
  contestId,
  game,
  editable,
  dict,
}: {
  contestId: string;
  /** The game as `page.tsx` read it — `game.needsBuild` is what this panel
   * offers to fix. */
  game: Game;
  /** `contentEditable(contest.status)` — false once the olympiad is running. */
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.game.build;
  const errors = dict.errors;
  const router = useRouter();

  const [refusalCode, setRefusalCode] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();

  const building = game.status === "building" || game.status === "pending";
  if (!editable || (!game.needsBuild && !building)) return null;

  function message(code: string): string {
    return (errors as Record<string, string>)[code] ?? errors.fallback;
  }

  function requestBuild() {
    setRefusalCode(null);
    startTransition(async () => {
      const result = await requestGameBuildAction(contestId);
      if (result.code) {
        setRefusalCode(result.code);
        return;
      }
      // The screen this panel sits on reads `game` from a server component
      // (page.tsx), and a plain Server Action call — unlike a <form
      // action={...}> submit — does not itself refresh that tree even though
      // the action revalidated the path: game-upload.tsx's own
      // `router.refresh()` after `completeGameUploadAction` is the same
      // fix for the same gap.
      router.refresh();
    });
  }

  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-3 border-t border-line pt-8">
      <p className="text-small text-ink-2">{building ? t.building : t.notice}</p>
      <button
        type="button"
        disabled={building || pending}
        className={cn(buttonVariants({ variant: "secondary" }))}
        onClick={requestBuild}
      >
        {t.action}
      </button>
      {refusalCode ? (
        <p role="alert" className="text-small text-bad">
          {message(refusalCode)}
        </p>
      ) : null}
    </div>
  );
}
