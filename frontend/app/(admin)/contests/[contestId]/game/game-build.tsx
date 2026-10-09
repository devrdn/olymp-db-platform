"use client";

import { useState, useTransition } from "react";
import { useRouter } from "next/navigation";

import { buttonVariants } from "@/components/ui/button";
import type { Game } from "@/lib/api/game";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { requestGameBuildAction } from "./actions";
import { useGamePoll } from "./game-poll";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * Rebuild control for a builder-sourced game, whose rows can only be typed
 * after the definition's first (empty) build. Scripts and dumps never need it
 * (`NeedsBuild` is never true for them).
 *
 * Shown for three states with different sentences: data changed since the build
 * (`needsBuild`), the build failed (the server accepts a rebuild of a failed
 * game), or a build is running. Hidden once the contest runs, where the API
 * refuses it.
 */
export function GameBuild({
  contestId,
  game: fromServer,
  editable,
  dict,
}: {
  contestId: string;
  /**
   * `game.needsBuild` is what this offers to fix; re-read after every
   * table-builder write via `router.refresh()`.
   */
  game: Game;
  /** False once the contest is running. */
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.game.build;
  const errors = dict.errors;
  const router = useRouter();

  const [refusalCode, setRefusalCode] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();

  // The poll's last answer and the prop it started against. A new prop object
  // comes from a fetch after the latest write, so it wins over an older polled
  // snapshot (which would hide the notice); reset during render, per React's
  // docs.
  const [polled, setPolled] = useState<Game | null>(null);
  const [seen, setSeen] = useState(fromServer);
  if (seen !== fromServer) {
    setSeen(fromServer);
    setPolled(null);
  }

  const game = polled ?? fromServer;
  const building = game.status === "building" || game.status === "pending";
  const failed = game.status === "failed";

  // Shares `GameEditor`'s and `GameUpload`'s timer, so every panel gets the
  // same snapshot.
  useGamePoll(contestId, building, setPolled);

  const builder = game.source === "builder";
  if (!editable || !builder || (!game.needsBuild && !failed && !building)) return null;

  function message(code: string): string {
    return messageForCode(code, errors);
  }

  function requestBuild() {
    setRefusalCode(null);
    startTransition(async () => {
      const result = await requestGameBuildAction(contestId);
      if (result.code) {
        setRefusalCode(result.code);
        return;
      }
      // A plain action call does not refresh the server tree even after
      // revalidating, unlike a form submit.
      router.refresh();
    });
  }

  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-3 border-t border-line pt-8">
      <p className="text-small text-ink-2">{building ? t.building : failed ? t.failed : t.notice}</p>
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
