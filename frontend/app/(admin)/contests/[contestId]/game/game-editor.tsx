"use client";

import { useActionState, useRef, useState } from "react";

import { CodeEditor } from "@/components/product/code-editor";
import { Tag } from "@/components/ui/tag";
import { buttonVariants } from "@/components/ui/button";
import { FALLBACK_MAX_GAME_SCRIPT_BYTES } from "@/lib/api/game-terms";
import type { Game } from "@/lib/api/game";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { saveGameScriptAction, type GameState } from "./actions";
import { useGamePoll } from "./game-poll";

/**
 * The screen an organiser writes their game on.
 *
 * The same CodeEditor a participant types queries into, for the same reason
 * it exists there: this is SQL, and SQL is read by its shape. Nothing about
 * the highlighting is olympiad-specific, so nothing here is a second copy of
 * it.
 *
 * Saving stores the script and starts a build somewhere else — the API
 * answers 202 — so this component's other job is to watch. It asks again
 * every two seconds while the build is running and stops the moment it is
 * not, because a page left polling for ever is a page that costs an idle
 * browser and an idle server something all afternoon.
 */
export function GameEditor({
  contestId,
  initial,
  initialScript,
  editable,
  dict,
}: {
  contestId: string;
  initial: Game;
  initialScript: string;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.game;
  const errors = dict.errors;

  const [state, save, saving] = useActionState<GameState, FormData>(saveGameScriptAction, {});
  const [polled, setPolled] = useState<Game | null>(null);

  // Derived, not synchronised. A save hands the build to a worker, so the
  // answer to "is it built" is somewhere else the moment the action returns —
  // and the server's own answer to that save was `pending`. Writing that into
  // state from an effect would be a render cascade for a value both sides
  // already agree on; this says the same thing without one, and holds whether
  // or not the action's revalidation has refreshed `initial` yet.
  const game: Game = polled ?? (state.saved ? { ...initial, status: "pending", building: true } : initial);

  // The mirror field FormData reads. CodeEditor draws the text; a real
  // textarea is what a form submits and what a browser restores across a soft
  // reload — the same arrangement the participant's console uses, and its own
  // doc comment explains why the two are separate.
  const mirrorRef = useRef<HTMLTextAreaElement>(null);
  const [bytes, setBytes] = useState(() => new TextEncoder().encode(initialScript).length);

  // Shared with GameUpload below it on the page rather than a timer of this
  // component's own: one request per tick for one answer, and both panels
  // handed the same snapshot so they cannot disagree about a build that has
  // just finished (useGamePoll's own doc).
  useGamePoll(contestId, game.building, setPolled);

  // The server's own ceiling, from the status it just answered with — never
  // a constant of this bundle's own (CLAUDE.md rule 11). The fallback is for
  // an API old enough not to send the field; zero means "it did not say".
  const maxBytes = game.maxScriptBytes > 0 ? game.maxScriptBytes : FALLBACK_MAX_GAME_SCRIPT_BYTES;
  const tooLong = bytes > maxBytes;

  return (
    <form action={save} className="flex flex-col gap-4">
      <input type="hidden" name="contestId" value={contestId} />
      <textarea
        ref={mirrorRef}
        name="script"
        defaultValue={initialScript}
        aria-hidden="true"
        tabIndex={-1}
        className="sr-only"
      />

      <div className="flex flex-wrap items-center gap-3">
        <GameStatusTag game={game} t={t} />
        {game.version > 0 ? (
          <span className="font-mono text-label text-ink-3">
            {t.version} {game.version}
          </span>
        ) : null}
        {game.database !== "" ? (
          <span className="font-mono text-label text-ink-3">{game.database}</span>
        ) : null}
      </div>

      {game.source === "file" ? (
        <p className="max-w-body text-small text-ink-2">{t.sourceFile}</p>
      ) : null}
      {game.source === "builder" ? (
        <p className="max-w-body text-small text-ink-2">{t.sourceBuilder}</p>
      ) : null}

      {game.status === "failed" && game.buildError !== "" ? (
        <div className="flex flex-col gap-2 border-l-2 border-bad pl-4">
          <p className="text-control text-ink">{t.buildError}</p>
          <p className="max-w-body text-small text-ink-2">{t.buildErrorLede}</p>
          <pre className="overflow-x-auto font-mono text-data text-bad">{game.buildError}</pre>
        </div>
      ) : null}

      <CodeEditor
        className="h-100 border border-line-2"
        ariaLabel={t.label}
        placeholder={t.placeholder}
        getInitialValue={() => mirrorRef.current?.value ?? ""}
        onChange={(text) => {
          if (mirrorRef.current) mirrorRef.current.value = text;
          setBytes(new TextEncoder().encode(text).length);
        }}
      />

      <div className="flex flex-wrap items-center gap-3">
        <button
          type="submit"
          disabled={!editable || saving || tooLong}
          className={cn(buttonVariants({ variant: "primary" }))}
        >
          {saving ? t.saving : t.save}
        </button>
        <span className={cn("font-mono text-label", tooLong ? "text-bad" : "text-ink-3")}>
          {t.size
            .replace("{n}", String(Math.ceil(bytes / 1024)))
            .replace("{max}", String(Math.floor(maxBytes / 1024)))}
        </span>
        {tooLong ? <span className="text-small text-bad">{t.tooLong}</span> : null}
        {!editable ? <span className="text-small text-ink-2">{t.frozen}</span> : null}
        <span role="status" aria-live="polite" className="text-small text-ink-2">
          {state.code ? (errors[state.code as keyof typeof errors] ?? state.code) : state.saved ? t.saved : ""}
        </span>
      </div>
    </form>
  );
}

/** The build's state, in the one word an organiser needs. */
function GameStatusTag({ game, t }: { game: Game; t: Dictionary["workspace"]["game"] }) {
  const tone =
    game.status === "ready"
      ? "good"
      : game.status === "failed"
        ? "bad"
        : game.building
          ? "warn"
          : "mute";
  return <Tag tone={tone}>{t.status[game.status]}</Tag>;
}
