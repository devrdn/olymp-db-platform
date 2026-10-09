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
    needsBuild: false,
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

  // "The build failed" alone gives the author nothing to act on.
  test("shows PostgreSQL's own words when the build failed", () => {
    show(game({ status: "failed", buildError: `ERROR: type "nosuchtype" does not exist` }));

    expect(screen.getByText(t.buildError)).toBeInTheDocument();
    expect(screen.getByText(/nosuchtype/)).toBeInTheDocument();
  });

  // The editor is empty for a file-sourced game, so only this note says where
  // the game came from.
  test("says the active game was built from an uploaded file, not this editor", () => {
    show(game({ status: "ready", version: 1, source: "file" }));

    expect(screen.getByText(t.sourceFile)).toBeInTheDocument();
  });

  test("says nothing about a file for a game authored right here", () => {
    show(game({ status: "ready", version: 1, source: "editor" }));

    expect(screen.queryByText(t.sourceFile)).not.toBeInTheDocument();
  });

  // Likewise for a builder-sourced game.
  test("says the active game was described with the table builder, not this editor", () => {
    show(game({ status: "ready", version: 1, source: "builder" }));

    expect(screen.getByText(t.sourceBuilder)).toBeInTheDocument();
    expect(screen.queryByText(t.sourceFile)).not.toBeInTheDocument();
  });

  // Replacing a game makes every participant's copy stale and remade.
  test("cannot be saved once the contest has started", () => {
    show(game({ status: "ready", version: 1 }), { editable: false });

    expect(screen.getByRole("button", { name: t.save })).toBeDisabled();
    expect(screen.getByText(t.frozen)).toBeInTheDocument();
  });

  // The ceiling comes from the server's `maxScriptBytes`, never a bundled
  // constant (CLAUDE.md rule 11); an otherwise unused number proves it.
  test("refuses to submit a script past the size limit the server published", () => {
    // Opened with the script rather than typed: typing it was slow enough to
    // disturb another suite's timing.
    show(game({ maxScriptBytes: 200 }), { script: "x".repeat(201) });

    expect(screen.getByRole("button", { name: t.save })).toBeDisabled();
    expect(screen.getByText(t.tooLong)).toBeInTheDocument();
  });

  // Exactly at the ceiling is accepted.
  test("accepts a script of exactly the size limit the server published", () => {
    show(game({ maxScriptBytes: 200 }), { script: "x".repeat(200) });

    expect(screen.getByRole("button", { name: t.save })).not.toBeDisabled();
    expect(screen.queryByText(t.tooLong)).not.toBeInTheDocument();
  });

  // An older API sends zero; fall back to the built-in ceiling rather than
  // refusing nothing.
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
    // Shows the save's `pending` answer without waiting for a poll.
    expect(screen.getByText(t.status.pending)).toBeInTheDocument();
  });

  test("asks again while the build runs, and stops once it is not running", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    show(game({ status: "building", building: true, version: 1 }));

    status.current = game({ status: "ready", building: false, version: 1, database: "game_tpl_cabc" });
    await vi.advanceTimersByTimeAsync(2100);

    await waitFor(() => expect(screen.getByText(t.status.ready)).toBeInTheDocument());

    const asked = gameStatusAction.mock.calls.length;
    // Polling must stop once the build ends.
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
