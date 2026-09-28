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
 * Asking for the game to be built again, and the one sentence that explains
 * why anybody would.
 *
 * The table builder's rows are stored after the build that would have loaded
 * them and cannot be stored before it — a row may only be typed into a table
 * the saved definition already names, and saving the definition is what
 * starts the build. So a builder game is normally built empty first and
 * filled afterwards, and this is how the filling reaches a database.
 *
 * Only for a builder-sourced game. A script and an uploaded dump are complete
 * at the moment they are saved: nothing can arrive after the build for the
 * build to have missed, and `NeedsBuild` is never true for either. Rendered
 * for all three, this put a second "Building…" line and a disabled button
 * beside `GameEditor`'s own status tag on two flows it has nothing to say
 * about.
 *
 * Three states put it on the screen, and they are three different sentences:
 * the data has moved on since the build (`needsBuild`), the build failed
 * (nothing was made at all, and `needsBuild` is false because it means
 * "stale"), or a build is running now. The failed one matters: `RequestBuild`
 * accepts a failed game on purpose, and without it a transient failure left
 * an organiser with their rows stored, no database, and no way back except
 * re-saving the definition — the workaround this feature exists to retire.
 *
 * Nothing is rendered once the contest is running. The API refuses the
 * request there (raising the version drops and remakes every participant's
 * copy), and a button that exists to be refused is worse than no button.
 */
export function GameBuild({
  contestId,
  game: fromServer,
  editable,
  dict,
}: {
  contestId: string;
  /** The game as `page.tsx` read it — `game.needsBuild` is what this panel
   * offers to fix. Re-read on every row written into the table builder
   * (`game-builder-table.tsx`'s own `router.refresh()`), which is what lets
   * the notice appear in the session the rows were typed in. */
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

  // What the poll last said, and the prop it was compared against.
  //
  // Two things describe this game: the poll below, while a build runs, and
  // the server component above, re-rendered whenever a row is written. A
  // snapshot polled before that write is the older of the two, and preferring
  // it would hide the very notice the write raises — this panel's own defect,
  // one layer up. A prop object that is not the one the poll was started
  // against is a newer answer by construction: the fetch that produced it ran
  // after the write that asked for it. React's own "adjust state while
  // rendering" is how that is dropped without a render cascade.
  const [polled, setPolled] = useState<Game | null>(null);
  const [seen, setSeen] = useState(fromServer);
  if (seen !== fromServer) {
    setSeen(fromServer);
    setPolled(null);
  }

  const game = polled ?? fromServer;
  const building = game.status === "building" || game.status === "pending";
  const failed = game.status === "failed";

  // The timer `GameEditor` and `GameUpload` are already on, not one of this
  // panel's own: one request per tick for one answer, and every panel handed
  // the same snapshot so they cannot disagree about a build that has just
  // finished (`useGamePoll`'s own doc). Without it this said "Building…"
  // under a status tag that already said "ready", and went on saying it until
  // the page was reloaded.
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
