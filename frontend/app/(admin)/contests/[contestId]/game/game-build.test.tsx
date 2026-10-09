import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { Game } from "@/lib/api/game";

import { GameBuild } from "./game-build";

const refresh = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

const requestGameBuildAction = vi.hoisted(() => vi.fn());
// The real `game-poll.ts`, driven through its one action, so this panel shares
// the status tag's snapshot.
const polled = vi.hoisted(() => ({ current: null as Game | null }));
const gameStatusAction = vi.hoisted(() => vi.fn(async () => polled.current));
vi.mock("./actions", () => ({ requestGameBuildAction, gameStatusAction }));

const contestId = "11111111-1111-1111-1111-111111111111";
const t = en.workspace.game.build;

const limits = { enabled: true, chunkBytes: 8388608, maxFileBytes: 4294967296 };

/** A `ready` builder game with no pending data; the only source this panel renders for. */
function game(overrides: Partial<Game> = {}): Game {
  return {
    status: "ready",
    version: 2,
    database: "contest_1",
    source: "builder",
    upload: undefined,
    buildError: "",
    scriptBytes: 0,
    maxScriptBytes: 512 * 1024,
    building: false,
    updatedAt: "2026-03-01T09:00:00Z",
    needsBuild: false,
    uploadLimits: limits,
    ...overrides,
  };
}

function show({
  initialGame,
  editable = true,
}: {
  initialGame?: Game;
  editable?: boolean;
} = {}) {
  return render(
    <GameBuild contestId={contestId} game={initialGame ?? game()} editable={editable} dict={en} />,
  );
}

beforeEach(() => {
  refresh.mockClear();
  requestGameBuildAction.mockReset();
  gameStatusAction.mockClear();
  polled.current = null;
});

afterEach(() => {
  vi.useRealTimers();
});

describe("GameBuild", () => {
  test("offers the build when the data has moved on without it", () => {
    show({ initialGame: game({ needsBuild: true }) });

    expect(screen.getByText(t.notice)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t.action })).toBeInTheDocument();
  });

  test("says nothing when the built game holds the current data", () => {
    const { container } = show({ initialGame: game({ needsBuild: false }) });

    expect(screen.queryByRole("button", { name: t.action })).not.toBeInTheDocument();
    expect(screen.queryByText(t.notice)).not.toBeInTheDocument();
    expect(container).toBeEmptyDOMElement();
  });

  test("offers nothing once the contest is running", () => {
    const { container } = show({ initialGame: game({ needsBuild: true }), editable: false });

    expect(screen.queryByRole("button", { name: t.action })).not.toBeInTheDocument();
    expect(container).toBeEmptyDOMElement();
  });

  test("disables the button while a build is running", () => {
    show({ initialGame: game({ status: "building", needsBuild: false, building: true }) });

    const button = screen.getByRole("button", { name: t.action });
    expect(button).toBeInTheDocument();
    expect(button).toBeDisabled();
    expect(screen.getByText(t.building)).toBeInTheDocument();
  });

  test("asks the server to build again and refreshes once it accepts", async () => {
    requestGameBuildAction.mockResolvedValueOnce({ value: game({ status: "pending", needsBuild: false }) });
    show({ initialGame: game({ needsBuild: true }) });

    await userEvent.click(screen.getByRole("button", { name: t.action }));

    expect(requestGameBuildAction).toHaveBeenCalledWith(contestId);
    expect(refresh).toHaveBeenCalled();
  });

  test("shows the server's own refusal instead of throwing", async () => {
    requestGameBuildAction.mockResolvedValueOnce({ code: "build_in_progress" });
    show({ initialGame: game({ needsBuild: true }) });

    await userEvent.click(screen.getByRole("button", { name: t.action }));

    expect(await screen.findByText(en.errors.build_in_progress)).toBeInTheDocument();
  });

  // Scripts and dumps are complete when saved, so `NeedsBuild` is builder-only
  // and the panel stays silent.
  test("says nothing about a game built from a script", () => {
    const { container } = show({
      initialGame: game({ source: "editor", status: "building", building: true }),
    });

    expect(container).toBeEmptyDOMElement();
  });

  test("says nothing about a game built from an uploaded dump", () => {
    const { container } = show({
      initialGame: game({ source: "file", status: "building", building: true }),
    });

    expect(container).toBeEmptyDOMElement();
  });

  // A failed builder game has stored rows but no database, and `NeedsBuild`
  // ("stale") is false; the API accepts a rebuild of a failed game.
  test("offers the build again after one failed, with a sentence of its own", () => {
    show({ initialGame: game({ status: "failed", needsBuild: false, buildError: "boom" }) });

    expect(screen.getByText(t.failed)).toBeInTheDocument();
    expect(screen.queryByText(t.notice)).not.toBeInTheDocument();
    const button = screen.getByRole("button", { name: t.action });
    expect(button).toBeEnabled();
  });

  test("offers nothing on a failed game once the contest is running", () => {
    const { container } = show({ initialGame: game({ status: "failed" }), editable: false });

    expect(container).toBeEmptyDOMElement();
  });

  // Follows the shared poll, so it never says "Building…" under a "ready" tag.
  test("follows the shared poll rather than contradicting the status beside it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    show({ initialGame: game({ status: "building", building: true, needsBuild: false }) });

    expect(screen.getByText(t.building)).toBeInTheDocument();

    polled.current = game({ status: "ready", building: false, needsBuild: false });
    await vi.advanceTimersByTimeAsync(2100);

    await waitFor(() => expect(screen.queryByText(t.building)).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: t.action })).not.toBeInTheDocument();
  });

  // A newer server prop (after another row is typed) must win over an older
  // polled snapshot, or the out-of-date notice would stay hidden.
  test("prefers a fresh snapshot from the server over one it polled earlier", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const building = game({ status: "building", building: true, needsBuild: false });
    const { rerender } = render(
      <GameBuild contestId={contestId} game={building} editable dict={en} />,
    );

    polled.current = game({ status: "ready", building: false, needsBuild: false });
    await vi.advanceTimersByTimeAsync(2100);
    await waitFor(() => expect(screen.queryByText(t.building)).not.toBeInTheDocument());

    rerender(
      <GameBuild
        contestId={contestId}
        game={game({ status: "ready", needsBuild: true })}
        editable
        dict={en}
      />,
    );

    expect(screen.getByText(t.notice)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t.action })).toBeEnabled();
  });
});
