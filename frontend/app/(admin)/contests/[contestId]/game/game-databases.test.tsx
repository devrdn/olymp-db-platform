import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { GameInstance, GameInstances } from "@/lib/api/game";

import { GameDatabases, readableSize } from "./game-databases";

type State = { saved?: boolean; code?: string };

const dropped = vi.hoisted(() => ({ current: { saved: true } as State }));
// Typed with the Server Action's own signature, so the assertion below reads
// the FormData it was given rather than casting its way to it.
const dropGameInstanceAction = vi.hoisted(() =>
  vi.fn<(previous: { saved?: boolean; code?: string }, form: FormData) => Promise<State>>(
    async () => dropped.current,
  ),
);

vi.mock("./actions", () => ({ dropGameInstanceAction }));

const contestId = "11111111-1111-1111-1111-111111111111";
const t = en.workspace.game.databases;

function instance(overrides: Partial<GameInstance> = {}): GameInstance {
  return {
    database: "game_c1_u1",
    spare: false,
    registrationId: "22222222-2222-2222-2222-222222222222",
    participant: "ivan",
    participantName: "Ivan Petrov",
    templateVersion: 3,
    status: "ready",
    sizeBytes: 4 * 1024 * 1024,
    sizeKnown: true,
    createdAt: "2026-03-01T09:00:00Z",
    updatedAt: "2026-03-01T09:00:00Z",
    ...overrides,
  };
}

function show(databases: Partial<GameInstances> = {}) {
  return render(
    <GameDatabases
      contestId={contestId}
      databases={{ instances: [instance()], truncated: false, ...databases }}
      locale="en"
      dict={en}
    />,
  );
}

beforeEach(() => {
  dropped.current = { saved: true };
  dropGameInstanceAction.mockClear();
  vi.spyOn(window, "confirm").mockReturnValue(true);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the contest's databases", () => {
  test("names the participant holding a copy, not only the database", () => {
    show();

    expect(screen.getByText("game_c1_u1")).toBeInTheDocument();
    expect(screen.getByText("Ivan Petrov")).toBeInTheDocument();
    expect(screen.getByText("ivan")).toBeInTheDocument();
  });

  test("marks an unclaimed copy as spare rather than leaving the holder blank", () => {
    show({ instances: [instance({ spare: true, registrationId: "", participant: "", participantName: "" })] });

    expect(screen.getByText(t.spare)).toBeInTheDocument();
  });

  // The row outlives the account on it, and inventing a name for somebody who
  // is gone would be inventing a record.
  test("says the account is gone rather than showing an empty holder", () => {
    show({ instances: [instance({ participant: "", participantName: "" })] });

    expect(screen.getByText(t.formerParticipant)).toBeInTheDocument();
  });

  // A size the game cluster could not give must never read as an empty
  // database, which is what a bare zero would say on this screen.
  test("says a size is unknown rather than showing it as zero", () => {
    show({ instances: [instance({ sizeBytes: 0, sizeKnown: false })] });

    expect(screen.getByText(t.sizeUnknown)).toBeInTheDocument();
  });

  test("a short list says so when there are more databases than it shows", () => {
    show({ truncated: true });

    expect(screen.getByText(t.truncated)).toBeInTheDocument();
  });

  test("offers no drop for a spare, which the pool tender replaces on its own", () => {
    show({ instances: [instance({ spare: true, participant: "", participantName: "" })] });

    expect(screen.queryByRole("button", { name: t.drop })).not.toBeInTheDocument();
  });

  test("offers no drop for a database that is already gone", () => {
    show({ instances: [instance({ status: "dropped" })] });

    expect(screen.queryByRole("button", { name: t.drop })).not.toBeInTheDocument();
  });

  // The mistake this guards against is pressing the button on the wrong row,
  // so the question has to name whose database it is.
  test("asks before dropping, naming the participant", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    show();

    await userEvent.click(screen.getByRole("button", { name: t.drop }));

    expect(confirm).toHaveBeenCalledWith(t.confirm.replace("{who}", "Ivan Petrov"));
    expect(dropGameInstanceAction).not.toHaveBeenCalled();
  });

  test("sends the database name when the question is answered yes", async () => {
    show();

    await userEvent.click(screen.getByRole("button", { name: t.drop }));

    expect(dropGameInstanceAction).toHaveBeenCalled();
    const form = dropGameInstanceAction.mock.calls[0][1];
    expect(form.get("database")).toBe("game_c1_u1");
    expect(form.get("contestId")).toBe(contestId);
  });

  test("shows the server's refusal on the row it happened to", async () => {
    dropped.current = { code: "game_instance_already_dropped" };
    show();

    await userEvent.click(screen.getByRole("button", { name: t.drop }));

    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText(en.errors.game_instance_already_dropped)).toBeInTheDocument();
  });

  test("says the contest has no databases rather than drawing an empty table", () => {
    show({ instances: [] });

    expect(screen.getByText(t.empty)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});

describe("readableSize", () => {
  test("scales to the unit a person reads", () => {
    expect(readableSize(0)).toBe("0 B");
    expect(readableSize(900)).toBe("900 B");
    expect(readableSize(4 * 1024 * 1024)).toBe("4.0 MiB");
    expect(readableSize(1536)).toBe("1.5 KiB");
    expect(readableSize(64 * 1024 * 1024)).toBe("64 MiB");
  });
});
