import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import type { StaffStandings } from "@/lib/api/leaderboard";
import { formatTime } from "@/lib/format/datetime";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: vi.fn() }) }));
vi.mock("./actions", () => ({ revealStandingsAction: vi.fn(async () => ({ revealedAt: "2026-09-20T13:00:00Z" })) }));

import { StaffStandingsView } from "./staff-standings";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const ID = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

function board(overrides: Partial<StaffStandings> = {}): StaffStandings {
  return {
    shown: { state: "frozen", frozenAt: "2026-09-20T11:30:00Z" },
    scoring: "points",
    freezeMin: 30,
    names: "login",
    revealedAt: undefined,
    generatedAt: "2026-09-20T12:00:00Z",
    truncated: false,
    rows: [
      { place: 1, login: "holmes", fullName: "Sherlock Holmes", deleted: false, disqualified: false, points: 30, solved: 3, lastScoredAt: undefined, winner: false },
      { place: 2, login: "moriarty", fullName: "James Moriarty", deleted: false, disqualified: true, points: 99, solved: 9, lastScoredAt: undefined, winner: false },
    ],
    ...overrides,
  };
}

function show(value: StaffStandings, status: "running" | "finished") {
  return render(<StaffStandingsView contestId={ID} status={status} standings={value} dict={dict} locale="en" />);
}

describe("the staff table", () => {
  const t = () => dict.leaderboard.staff;

  test("names people twice over, marks the disqualified, and says what everybody else sees", () => {
    show(board(), "running");

    const row = screen.getByText("James Moriarty").closest("tr") as HTMLElement;
    expect(within(row).getByText("moriarty")).toBeInTheDocument();
    expect(within(row).getByText(t().disqualified)).toBeInTheDocument();
    expect(
      screen.getByText(t().shownFrozen.replace("{time}", formatTime("2026-09-20T11:30:00Z", { locale: "en" }))),
    ).toBeInTheDocument();
  });

  test("links the public page", () => {
    show(board(), "running");

    expect(screen.getByRole("link", { name: t().publicPage })).toHaveAttribute("href", `/contests/${ID}/leaderboard`);
  });

  test("offers the reveal only once a frozen contest has finished", () => {
    const { unmount } = show(board(), "running");
    expect(screen.queryByRole("button", { name: t().reveal })).not.toBeInTheDocument();
    unmount();

    const { unmount: again } = show(board({ freezeMin: null, shown: { state: "final", frozenAt: undefined } }), "finished");
    expect(screen.queryByRole("button", { name: t().reveal })).not.toBeInTheDocument();
    again();

    show(board({ revealedAt: "2026-09-20T13:00:00Z", shown: { state: "final", frozenAt: undefined } }), "finished");
    expect(screen.queryByRole("button", { name: t().reveal })).not.toBeInTheDocument();
    expect(
      screen.getByText(t().revealedAt.replace("{time}", formatTime("2026-09-20T13:00:00Z", { locale: "en" }))),
    ).toBeInTheDocument();
  });

  test("asks before revealing, because it cannot be undone", async () => {
    const user = userEvent.setup();
    show(board(), "finished");

    await user.click(screen.getByRole("button", { name: t().reveal }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(t().revealBody)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: t().revealConfirm })).toBeInTheDocument();
  });
});
