import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { Game } from "@/lib/api/game";

import { GameBuild } from "./game-build";

const refresh = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

const requestGameBuildAction = vi.hoisted(() => vi.fn());
vi.mock("./actions", () => ({ requestGameBuildAction }));

const contestId = "11111111-1111-1111-1111-111111111111";
const t = en.workspace.game.build;

const limits = { enabled: true, chunkBytes: 8388608, maxFileBytes: 4294967296 };

/** A `ready` game whose data has not moved on — most tests start from this
 * and override the one field they care about. */
function game(overrides: Partial<Game> = {}): Game {
  return {
    status: "ready",
    version: 2,
    database: "contest_1",
    source: "editor",
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
});
