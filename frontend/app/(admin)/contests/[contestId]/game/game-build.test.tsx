import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { Game } from "@/lib/api/game";

import { GameBuild } from "./game-build";

const refresh = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

const requestGameBuildAction = vi.hoisted(() => vi.fn());
// The same poll `GameEditor` and `GameUpload` are subscribed to — the real
// `game-poll.ts`, driven through the one action it calls, so what this panel
// shows while a build runs is the same snapshot the status tag beside it
// shows rather than a second account of the same build.
const polled = vi.hoisted(() => ({ current: null as Game | null }));
const gameStatusAction = vi.hoisted(() => vi.fn(async () => polled.current));
vi.mock("./actions", () => ({ requestGameBuildAction, gameStatusAction }));

const contestId = "11111111-1111-1111-1111-111111111111";
const t = en.workspace.game.build;

const limits = { enabled: true, chunkBytes: 8388608, maxFileBytes: 4294967296 };

/** A `ready` builder game whose data has not moved on — most tests start from
 * this and override the one field they care about.
 *
 * Builder-sourced, because that is the only source this panel renders for:
 * a script and an uploaded dump are complete the moment they are saved, and
 * neither has anything that can arrive after the build. */
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

  // A script and an uploaded dump are complete at the moment they are saved:
  // there is nothing that can arrive afterwards for a build to have missed,
  // which is why `NeedsBuild` is a builder-only idea and why this panel has
  // nothing to say on either of the other two flows. Rendered there, it put a
  // second "Building…" line and a disabled button beside `GameEditor`'s own
  // status tag on screens this feature does not touch.
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

  // A failed builder game is exactly where the button is needed most: the
  // rows are typed and stored, no database holds them, and `NeedsBuild` is
  // false because it means "stale", which a game that was never built is not.
  // The API accepts the request on a failed game on purpose
  // (`RequestBuild`'s own `status IN ('ready','failed')`), so the only way
  // back used to be re-saving the definition — the workaround this whole
  // feature exists to retire.
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

  // The build this panel starts is watched by the poll `GameEditor` and
  // `GameUpload` are already subscribed to, not by a timer of its own. Left
  // unsubscribed, the panel said "Building…" under a status tag that already
  // said "ready", and went on saying it until somebody reloaded the page.
  test("follows the shared poll rather than contradicting the status beside it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    show({ initialGame: game({ status: "building", building: true, needsBuild: false }) });

    expect(screen.getByText(t.building)).toBeInTheDocument();

    polled.current = game({ status: "ready", building: false, needsBuild: false });
    await vi.advanceTimersByTimeAsync(2100);

    await waitFor(() => expect(screen.queryByText(t.building)).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: t.action })).not.toBeInTheDocument();
  });

  // The poll's snapshot must never outrank a newer one from the server. A
  // build finishes, this panel holds a `ready` game with no data mark, and
  // then the organiser types another row: `page.tsx` re-renders with the
  // template now marked out of date, and a panel still reading its own older
  // snapshot would go on saying nothing — the silence this whole feature
  // exists to end, reintroduced after the first successful build.
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
