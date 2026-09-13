import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

import type { ContestSummary } from "@/lib/api/contests";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The join control submits to a Server Action; importing it for real pulls in
// `next/headers`. What the register decides — whether to offer the control at
// all — is what is under test.
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
  // Spelled out rather than omitted: the schema's transform produces the key
  // whether or not the API sent one, so a factory that leaves it out is not
  // building the shape the component actually receives.
  description: undefined,
  startsAt: "2026-05-14T07:00:00Z",
  endsAt: "2026-05-14T10:00:00Z",
  enrolled: false,
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
      // Shaped as a page shapes it: the dictionary holds the label, the
      // destination is the application's and not the translator's.
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
   * The API decides who may join; the register decides only what is worth
   * offering. A button on a contest nobody can self-join is a control that
   * exists to be refused, and the state column already says why.
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

  /**
   * A contest already under way is the game loop's to open, and that is step 5.
   * Offering "join" here would promise a door this build does not have.
   */
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

  /**
   * The screen has no filters, so there is no control to clear. Offering a
   * reset here would be offering a way out of a state nothing led into — the
   * link that is offered goes to the open list, which is a next step and not
   * an undo.
   */
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
    // The catalogue lists open contests including the ones already joined —
    // hiding those would answer "what is there" incompletely, and somebody who
    // cannot find a familiar name concludes their registration was lost. So
    // the row has to distinguish them. Offering the button and letting the API
    // answer `already_enrolled` was tolerable while both kinds shared one
    // screen; on a catalogue it turns an ordinary state into an error message.
    render_([contest({ enrolled: true })]);

    expect(screen.getByText(en.participant.enrolled)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: en.participant.join }),
    ).not.toBeInTheDocument();
  });

  test("still says so when the contest is no longer taking signups", () => {
    // A running or finished contest cannot be joined by anyone. For somebody
    // who is on it, "nothing to do yet" would read as though they were not.
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
  // Without this the console existed and nothing led to it, which is the same
  // as it not existing.
  test("a contest that is running and enrolled offers a way in", () => {
    render_([contest({ id: "c1", status: "running", enrolled: true })]);

    expect(screen.getByRole("link", { name: en.participant.openConsole })).toHaveAttribute(
      "href",
      "/contests/c1/play",
    );
  });

  // A contest that has not started has no console, and one that has finished
  // has no console left. Offering the door either side of the contest is
  // offering a refusal.
  test.each(["published", "finished"] as const)("but %s does not", (status) => {
    render_([contest({ id: "c1", status, enrolled: true })]);

    expect(screen.queryByRole("link", { name: en.participant.openConsole })).not.toBeInTheDocument();
  });

  // Enrolment is the other half: a running contest somebody is not in is not
  // a contest they may walk into.
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
