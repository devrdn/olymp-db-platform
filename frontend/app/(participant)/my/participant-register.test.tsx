import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

import type { ContestSummary } from "@/lib/api/contests";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action pulls in `next/headers`; only whether the control
// is offered is under test.
vi.mock("./actions", () => ({ enrollAction: vi.fn() }));

import { ParticipantRegister } from "./participant-register";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

const contest = (over: Partial<ContestSummary> = {}): ContestSummary => ({
  id: "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01",
  status: "published",
  enrollment: "open",
  questionMode: "multi",
  lang: "en",
  title: "The Greenhouse",
  // Spelled out: the schema's transform always produces the key, so the
  // fixture matches what the component receives.
  description: undefined,
  startsAt: "2026-05-14T07:00:00Z",
  endsAt: "2026-05-14T10:00:00Z",
  enrolled: false,
  scoring: "points",
  icpcPenaltyMin: 20,
  // Spelled out for the same reason as `description`.
  coverHash: "",
  coverAttribution: "",
  ...over,
});

const render_ = (contests: ContestSummary[]) =>
  render(
    <ParticipantRegister
      contests={contests}
      total={contests.length}
      dict={en}
      locale="en"
      heading={en.participant.mine.heading}
      countLabel={en.participant.mine.countLabel}
      // As a page passes it: the label from the dictionary, the destination
      // from the app.
      empty={{
        title: en.participant.mine.empty.title,
        body: en.participant.mine.empty.body,
        action: { label: en.participant.mine.empty.action, href: "/open" },
      }}
    />,
  );

describe("the way in", () => {
  test("is offered on an open contest that has been published", () => {
    render_([contest()]);

    expect(screen.getByRole("button", { name: en.participant.join })).toBeInTheDocument();
  });

  /**
   * The API decides who may join; the register only withholds a button that
   * could only be refused. The state column already says why.
   */
  test("is withheld from a contest that is by invitation", () => {
    render_([contest({ enrollment: "invite_only" })]);

    expect(screen.queryByRole("button", { name: en.participant.join })).not.toBeInTheDocument();
    expect(screen.getByText(en.participant.byInvitation)).toBeInTheDocument();
  });

  test("is withheld from a contest that has finished", () => {
    render_([contest({ status: "finished" })]);

    expect(screen.queryByRole("button", { name: en.participant.join })).not.toBeInTheDocument();
  });

  /** Joining is offered only before the contest starts. */
  test("is withheld from a contest already under way", () => {
    render_([contest({ status: "running" })]);

    expect(screen.queryByRole("button", { name: en.participant.join })).not.toBeInTheDocument();
  });
});

describe("the register itself", () => {
  test("is a table, so a column can be compared down its length", () => {
    render_([contest(), contest({ id: "aa1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d02", title: "The Pier" })]);

    expect(screen.getAllByRole("row")).toHaveLength(3); // one head, two contests
  });

  test("says nothing is waiting rather than showing an empty table", () => {
    render_([]);

    expect(screen.getByText(en.participant.mine.empty.title)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  /** There are no filters, so no reset; the offered link leads to the open list. */
  test("offers no filter reset it could not honour", () => {
    render_([]);

    expect(
      screen.queryByRole("link", { name: en.contests.emptyFiltered.reset }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: en.participant.mine.empty.action }),
    ).toHaveAttribute("href", "/open");
  });

  test("names the contest and the state it is in", () => {
    render_([contest({ status: "running" })]);

    const row = screen.getAllByRole("row")[1];
    expect(within(row).getByText("The Greenhouse")).toBeInTheDocument();
    expect(within(row).getByText(en.contests.status.running)).toBeInTheDocument();
  });
});

describe("a contest the visitor is already on", () => {
  test("says so instead of offering to join it again", () => {
    // The catalogue lists joined contests too, so the row must say so rather
    // than offer a button that answers `already_enrolled`.
    render_([contest({ enrolled: true })]);

    expect(screen.getByText(en.participant.enrolled)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: en.participant.join }),
    ).not.toBeInTheDocument();
  });

  test("still says so when the contest is no longer taking signups", () => {
    // "Nothing to do yet" would read as though they were not on it.
    render_([contest({ enrolled: true, status: "finished", enrollment: "invite_only" })]);

    expect(screen.getByText(en.participant.enrolled)).toBeInTheDocument();
    expect(screen.queryByText(en.participant.byInvitation)).not.toBeInTheDocument();
  });

  test("offers the way in to somebody who is not on it", () => {
    render_([contest({ enrolled: false })]);

    expect(screen.getByRole("button", { name: en.participant.join })).toBeInTheDocument();
    expect(screen.queryByText(en.participant.enrolled)).not.toBeInTheDocument();
  });
});

describe("the way into a running contest", () => {
  // The only way into the console from the register.
  test("a contest that is running and enrolled offers a way in", () => {
    render_([contest({ id: "c1", status: "running", enrolled: true })]);

    expect(screen.getByRole("link", { name: en.participant.openConsole })).toHaveAttribute(
      "href",
      "/contests/c1/play",
    );
  });

  // Before the start there is no console, after the finish none is left.
  test.each(["published", "finished"] as const)("but %s does not", (status) => {
    render_([contest({ id: "c1", status, enrolled: true })]);

    expect(screen.queryByRole("link", { name: en.participant.openConsole })).not.toBeInTheDocument();
  });

  // Running but not enrolled is not a way in either.
  test("nor does a running contest the viewer is not in", () => {
    render_([contest({ id: "c1", status: "running", enrolled: false })]);

    expect(screen.queryByRole("link", { name: en.participant.openConsole })).not.toBeInTheDocument();
  });
});

describe("the way to a contest's table", () => {
  const label = (title: string) => en.leaderboard.openLabel.replace("{title}", title);

  test("sits beside the way in once the contest is under way", () => {
    render_([contest({ status: "running", enrolled: true })]);

    expect(screen.getByRole("link", { name: en.participant.openConsole })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: label("The Greenhouse") })).toHaveAttribute(
      "href",
      "/contests/6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01/leaderboard",
    );
  });

  test("stays after the contest has finished, when the result is what people come back for", () => {
    render_([contest({ status: "finished", enrolled: true })]);

    expect(screen.getByRole("link", { name: label("The Greenhouse") })).toBeInTheDocument();
  });

  test("is not offered before there is a table to see", () => {
    render_([contest({ status: "published" })]);

    expect(screen.queryByRole("link", { name: label("The Greenhouse") })).not.toBeInTheDocument();
  });
});
