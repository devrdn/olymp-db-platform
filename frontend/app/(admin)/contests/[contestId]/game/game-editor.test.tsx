import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { Game } from "@/lib/api/game";
import { FALLBACK_MAX_GAME_SCRIPT_BYTES } from "@/lib/api/game-terms";

import { GameEditor } from "./game-editor";

const saved = vi.hoisted(() => ({ current: { saved: true } as { saved?: boolean; code?: string } }));
const status = vi.hoisted(() => ({ current: null as Game | null }));
const saveGameScriptAction = vi.hoisted(() => vi.fn(async () => saved.current));
const gameStatusAction = vi.hoisted(() => vi.fn(async () => status.current));

vi.mock("./actions", () => ({ saveGameScriptAction, gameStatusAction }));

function game(overrides: Partial<Game> = {}): Game {
  return {
    status: "absent",
    version: 0,
    database: "",
    source: "editor",
    upload: undefined,
    buildError: "",
    scriptBytes: 0,
    maxScriptBytes: 512 * 1024,
    building: false,
    updatedAt: "",
    uploadLimits: { enabled: false, chunkBytes: 0, maxFileBytes: 0 },
    ...overrides,
  };
}

function show(initial: Game, { editable = true, script = "" } = {}) {
  return render(
    <GameEditor
      contestId="11111111-1111-1111-1111-111111111111"
      initial={initial}
      initialScript={script}
      editable={editable}
      dict={en}
    />,
  );
}

beforeEach(() => {
  saved.current = { saved: true };
  status.current = null;
  gameStatusAction.mockClear();
});

afterEach(() => {
  vi.useRealTimers();
});

const t = en.workspace.game;

describe("the game editor", () => {
  test("says a contest has no game yet rather than looking empty", () => {
    show(game());

    expect(screen.getByText(t.status.absent)).toBeInTheDocument();
  });

  // Whoever wrote the script is the person who has to fix it, and "the build
  // failed" tells them nothing they can act on.
  test("shows PostgreSQL's own words when the build failed", () => {
    show(game({ status: "failed", buildError: `ERROR: type "nosuchtype" does not exist` }));

    expect(screen.getByText(t.buildError)).toBeInTheDocument();
    expect(screen.getByText(/nosuchtype/)).toBeInTheDocument();
  });

  // Without this note, a reloaded page has no way to tell a file-sourced
  // game apart from one authored right here — the editor below is empty
  // either way (`provisioning.Template.Script` is empty for a file-sourced
  // game), so nothing else on this screen says the active game did not come
  // from what is typed into it.
  test("says the active game was built from an uploaded file, not this editor", () => {
    show(game({ status: "ready", version: 1, source: "file" }));

    expect(screen.getByText(t.sourceFile)).toBeInTheDocument();
  });

  test("says nothing about a file for a game authored right here", () => {
    show(game({ status: "ready", version: 1, source: "editor" }));

    expect(screen.queryByText(t.sourceFile)).not.toBeInTheDocument();
  });

  // The third source (`GAME_SOURCES` in `lib/api/game.ts` — this game's own
  // definition editor is `game-builder.tsx`) gets the identical note
  // `sourceFile` gives a file-sourced game, for the identical reason: this
  // screen's own textarea is empty either way, and nothing else says the
  // active game did not come from what is typed into it.
  test("says the active game was described with the table builder, not this editor", () => {
    show(game({ status: "ready", version: 1, source: "builder" }));

    expect(screen.getByText(t.sourceBuilder)).toBeInTheDocument();
    expect(screen.queryByText(t.sourceFile)).not.toBeInTheDocument();
  });

  // Replacing a game raises the template's version, which makes every
  // participant's copy stale — and a stale copy is dropped and made again.
  test("cannot be saved once the contest has started", () => {
    show(game({ status: "ready", version: 1 }), { editable: false });

    expect(screen.getByRole("button", { name: t.save })).toBeDisabled();
    expect(screen.getByText(t.frozen)).toBeInTheDocument();
  });

  // The server refuses it too; saying so before the request is made is the
  // difference between a limit and a rejection.
  //
  // The ceiling comes from the status the server just answered with
  // (`maxScriptBytes`), never from a constant in this bundle — CLAUDE.md
  // rule 11. A number nothing else in this file uses is what proves it: a
  // screen that had kept its own copy would take a 200-byte script happily.
  test("refuses to submit a script past the size limit the server published", () => {
    // Measured from the script the screen opens on, rather than typed in.
    // Pasting the whole thing through userEvent took a second of the suite's
    // time to prove a rule that is true the moment the page renders — and
    // that second was enough to push another suite's own timing over.
    show(game({ maxScriptBytes: 200 }), { script: "x".repeat(201) });

    expect(screen.getByRole("button", { name: t.save })).toBeDisabled();
    expect(screen.getByText(t.tooLong)).toBeInTheDocument();
  });

  // Exactly at the published ceiling is still accepted — the refusal is one
  // byte past it, not near it.
  test("accepts a script of exactly the size limit the server published", () => {
    show(game({ maxScriptBytes: 200 }), { script: "x".repeat(200) });

    expect(screen.getByRole("button", { name: t.save })).not.toBeDisabled();
    expect(screen.queryByText(t.tooLong)).not.toBeInTheDocument();
  });

  // An API old enough not to publish the field sends zero, and refusing
  // nothing at all would be worse than refusing by yesterday's figure.
  test("falls back to the built-in ceiling when the server published none", () => {
    show(game({ maxScriptBytes: 0 }), { script: "x".repeat(FALLBACK_MAX_GAME_SCRIPT_BYTES + 1) });

    expect(screen.getByRole("button", { name: t.save })).toBeDisabled();
    expect(screen.getByText(t.tooLong)).toBeInTheDocument();
  });

  test("a saved script reports that the build has started", async () => {
    const user = userEvent.setup();
    show(game(), { script: "SELECT 1" });

    await user.click(screen.getByRole("button", { name: t.save }));

    await waitFor(() => expect(screen.getByText(t.saved)).toBeInTheDocument());
    // The server's own answer to that save was `pending`, and the screen says
    // so without waiting for a poll.
    expect(screen.getByText(t.status.pending)).toBeInTheDocument();
  });

  test("asks again while the build runs, and stops once it is not running", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    show(game({ status: "building", building: true, version: 1 }));

    status.current = game({ status: "ready", building: false, version: 1, database: "game_tpl_cabc" });
    await vi.advanceTimersByTimeAsync(2100);

    await waitFor(() => expect(screen.getByText(t.status.ready)).toBeInTheDocument());

    const asked = gameStatusAction.mock.calls.length;
    // A page left polling for ever costs an idle browser and an idle server
    // something all afternoon.
    await vi.advanceTimersByTimeAsync(6000);
    expect(gameStatusAction.mock.calls.length).toBe(asked);
  });

  test("a refusal is shown in the organiser's own language, not as a code", async () => {
    const user = userEvent.setup();
    saved.current = { code: "game_not_editable" };
    show(game(), { script: "SELECT 1" });

    await user.click(screen.getByRole("button", { name: t.save }));

    await waitFor(() => expect(screen.getByText(en.errors.game_not_editable)).toBeInTheDocument());
  });
});
