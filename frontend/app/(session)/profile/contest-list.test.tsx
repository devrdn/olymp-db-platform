import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { ProfileContest, ProfileContests } from "@/lib/api/profile";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ContestList } from "./contest-list";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

const finished = (over: Partial<ProfileContest> = {}): ProfileContest => ({
  contestId: "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01",
  title: "The Greenhouse",
  status: "finished",
  startsAt: "2026-05-14T07:00:00Z",
  endsAt: "2026-05-14T10:00:00Z",
  registrationStatus: "finished",
  over: true,
  result: {
    scoring: "points",
    points: 60,
    solved: 3,
    penalty: undefined,
    state: "final",
    placeOpen: true,
  },
  ...over,
});

const list = (items: ProfileContest[], truncated = false): ProfileContests => ({
  items,
  truncated,
});

const render_ = (contests: ProfileContests | null) =>
  render(<ContestList contests={contests} dict={en} locale="en" />);

describe("a contest that has ended", () => {
  test("shows the participant's own result and leads to its report", () => {
    render_(list([finished()]));

    const row = screen.getByRole("listitem");
    expect(within(row).getByText("60")).toBeInTheDocument();
    expect(within(row).getByText(en.profile.contests.points)).toBeInTheDocument();

    const link = within(row).getByRole("link", {
      name: en.profile.contests.report.replace("{title}", "The Greenhouse"),
    });
    expect(link).toHaveAttribute("href", "/profile/contests/6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01");
  });

  // ICPC writes no points at all: the result there is how many questions were
  // solved and what the wrong attempts cost. A row printing "0 points" over
  // four solved questions would be reporting the mode's own convention as a
  // score.
  test("under ICPC scoring counts solved questions and penalty, never points", () => {
    render_(
      list([
        finished({
          title: "Winter ICPC",
          result: {
            scoring: "icpc",
            points: 0,
            solved: 4,
            penalty: 87,
            state: "final",
            placeOpen: true,
          },
        }),
      ]),
    );

    const row = screen.getByRole("listitem");
    expect(within(row).getByText("4")).toBeInTheDocument();
    expect(within(row).getByText(en.profile.contests.solved)).toBeInTheDocument();
    expect(within(row).getByText("87")).toBeInTheDocument();
    expect(within(row).getByText(en.profile.contests.penalty)).toBeInTheDocument();
    expect(within(row).queryByText(en.profile.contests.points)).not.toBeInTheDocument();
  });

  // The list carries no place at all, and says so rather than leaving a gap:
  // a frozen table is not a missing result, it is a result that is not public
  // yet. The profile does not go round the freeze.
  test("says the place is not there yet while the table is closed", () => {
    render_(
      list([
        finished({
          result: {
            scoring: "points",
            points: 60,
            solved: 3,
            penalty: undefined,
            state: "frozen",
            placeOpen: false,
          },
        }),
      ]),
    );

    expect(screen.getByText(en.profile.contests.placePending)).toBeInTheDocument();
  });

  // A contest that never opened is not a frozen one, and the sentence a
  // frozen row gets would promise a table an organiser is about to reveal.
  // Somebody disqualified before the window opened is finished with the
  // contest, so this is the row they are shown: their own nothing, and a
  // plain explanation.
  test("says the table has not opened when the contest never started", () => {
    render_(
      list([
        finished({
          status: "published",
          registrationStatus: "disqualified",
          result: {
            scoring: "points",
            points: 0,
            solved: 0,
            penalty: undefined,
            state: "not_started",
            placeOpen: false,
          },
        }),
      ]),
    );

    expect(screen.getByText(en.profile.contests.placeNotStarted)).toBeInTheDocument();
    expect(screen.queryByText(en.profile.contests.placePending)).not.toBeInTheDocument();
  });

  test("keeps quiet about the place once the table is open", () => {
    render_(list([finished()]));

    expect(screen.queryByText(en.profile.contests.placePending)).not.toBeInTheDocument();
  });

  // A contest can be over for this participant — their own timer ran out, or
  // they were disqualified — while the clock says it is still running. What
  // such a row must never do is fall back to the sentence a contest still to
  // come gets: "starts" on something already finished is the one reading that
  // is certainly wrong.
  test("stays quiet rather than announcing a start it is past", () => {
    render_(
      list([
        finished({ title: "Cut short", status: "running", over: true, result: undefined }),
      ]),
    );

    const row = screen.getByRole("listitem");
    expect(within(row).queryByText(/Starts/)).not.toBeInTheDocument();
    expect(
      within(row).getByRole("link", {
        name: en.profile.contests.report.replace("{title}", "Cut short"),
      }),
    ).toBeInTheDocument();
  });

  test("marks a disqualified registration", () => {
    render_(list([finished({ registrationStatus: "disqualified" })]));

    expect(screen.getByText(en.profile.contests.disqualified)).toBeInTheDocument();
  });
});

describe("a contest that is running", () => {
  const running = finished({
    contestId: "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d02",
    title: "Spring open",
    status: "running",
    registrationStatus: "active",
    over: false,
    result: undefined,
  });

  test("offers the way back in and nothing else", () => {
    render_(list([running]));

    const row = screen.getByRole("listitem");
    const link = within(row).getByRole("link", { name: en.profile.contests.enter });
    expect(link).toHaveAttribute("href", "/contests/6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d02/play");

    // Nothing about what is happening inside it: no result, no report. What a
    // participant needs mid-contest is on the contest's own screen, under
    // that screen's rules.
    expect(within(row).queryByText(en.profile.contests.points)).not.toBeInTheDocument();
    expect(within(row).queryByText(en.profile.contests.solved)).not.toBeInTheDocument();
    expect(
      within(row).queryByRole("link", {
        name: en.profile.contests.report.replace("{title}", "Spring open"),
      }),
    ).not.toBeInTheDocument();
  });
});

describe("a contest still to come", () => {
  test("says when it starts, and offers nothing to press", () => {
    render_(
      list([
        finished({
          contestId: "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d03",
          title: "Autumn qualifier",
          status: "published",
          registrationStatus: "registered",
          over: false,
          result: undefined,
        }),
      ]),
    );

    const row = screen.getByRole("listitem");
    expect(within(row).getByText(/Starts/)).toBeInTheDocument();
    expect(within(row).queryByRole("link")).not.toBeInTheDocument();
  });
});

describe("the list itself", () => {
  test("explains an account that has taken part in nothing", () => {
    render_(list([]));

    expect(screen.getByText(en.profile.contests.empty.title)).toBeInTheDocument();
    expect(screen.getByText(en.profile.contests.empty.body)).toBeInTheDocument();
    expect(screen.queryByRole("listitem")).not.toBeInTheDocument();
  });

  test("says when the account has more contests than the list carries", () => {
    render_(list([finished()], true));

    expect(
      screen.getByText(en.profile.contests.truncated.replace("{count}", "1")),
    ).toBeInTheDocument();
  });

  // The larger of the page's two reads, and still not the page: the header
  // above it is what a failed list must not take down with it.
  test("gives up in one line when the read failed", () => {
    render_(null);

    expect(screen.getByRole("alert")).toHaveTextContent(en.profile.contests.failed);
    expect(screen.queryByRole("listitem")).not.toBeInTheDocument();
  });
});
