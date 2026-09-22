import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { PublicContest } from "@/lib/api/showcase";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { RecentContests } from "./recent-contests";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

/** A contest that is on right now, with only the disputed field to state. */
function running(over: Partial<PublicContest> = {}): PublicContest {
  return {
    id: "01JB0000000000000000000001",
    title: "Spring round",
    status: "running",
    startsAt: "2026-03-14T08:00:00Z",
    endsAt: "2026-03-14T11:00:00Z",
    tableOpen: true,
    ...over,
  };
}

describe("the contest list", () => {
  /**
   * The row offers the table because the table is open, never because the
   * contest exists. A link to a leaderboard that refuses the reader is worse
   * than no link: they have to follow it to find out.
   */
  test("leads to the public table only where there is one to read", () => {
    render(<RecentContests contests={[running({ tableOpen: false })]} dict={en} locale="en" />);
    expect(screen.queryByRole("link", { name: en.home.contests.table })).toBeNull();
  });

  test("leads to the public table where there is one", () => {
    render(<RecentContests contests={[running()]} dict={en} locale="en" />);
    expect(screen.getByRole("link", { name: en.home.contests.table })).toHaveAttribute(
      "href",
      `/contests/${running().id}/leaderboard`,
    );
  });

  /**
   * Six is the API's own ceiling, but a list that trusted it would print
   * however many rows a changed server sent onto a page whose whole argument
   * is that it is short.
   */
  test("shows at most six, and the freshest of them", () => {
    const many = Array.from({ length: 9 }, (_, index) =>
      running({ id: `contest-${index}`, title: `Round ${index}` }),
    );
    render(<RecentContests contests={many} dict={en} locale="en" />);

    const rows = screen.getAllByRole("listitem");
    expect(rows).toHaveLength(6);
    expect(rows[0]).toHaveTextContent("Round 0");
    expect(screen.queryByText("Round 6")).toBeNull();
  });

  test("marks the one that is on right now, and says what each state is", () => {
    render(
      <RecentContests
        contests={[running(), running({ id: "b", title: "Autumn round", status: "finished" })]}
        dict={en}
        locale="en"
      />,
    );

    const rows = screen.getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent(en.contests.status.running);
    expect(rows[1]).toHaveTextContent(en.contests.status.finished);
    // The accent dot belongs to what is happening now and to nothing else.
    expect(rows[0].querySelectorAll(".bg-accent")).toHaveLength(1);
    expect(rows[1].querySelectorAll(".bg-accent")).toHaveLength(0);
  });

  /**
   * An empty list and a failed read look the same to somebody who has just
   * arrived, and the sentence is written to be true of both. What neither may
   * produce is an empty table with a heading over it: a screen that is
   * accurate and leaves the reader nothing to do is half a state.
   */
  test.each([
    ["nothing to show", [] as PublicContest[]],
    ["a failed read", null],
  ])("explains %s and names the way on, rather than ruling an empty table", (_name, contests) => {
    render(<RecentContests contests={contests} dict={en} locale="en" />);

    expect(screen.getByText(en.home.contests.empty.body)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.home.contests.empty.action })).toHaveAttribute(
      "href",
      "/open",
    );
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
  });

  /** The hero's second action points here, so the section has to be here. */
  test("carries the anchor the hero points at", () => {
    const { container } = render(<RecentContests contests={[running()]} dict={en} locale="en" />);
    expect(container.querySelector("#contests")).not.toBeNull();
  });
});
