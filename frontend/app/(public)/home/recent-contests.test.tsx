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
    // No picture unless a test says otherwise: that is the state an
    // installation opens in, and the one the drawn cover exists for.
    coverHash: undefined,
    coverAttribution: undefined,
    ...over,
  };
}

/** The card of the one contest a test rendered. */
function card(): HTMLElement {
  return screen.getAllByRole("listitem")[0];
}

describe("the contest list", () => {
  /**
   * The row offers the table because the table is open, never because the
   * contest exists. A link to a leaderboard that refuses the reader is worse
   * than no link: they have to follow it to find out.
   */
  test("leads to the public table only where there is one to read", () => {
    render(<RecentContests signedIn={false} contests={[running({ tableOpen: false })]} dict={en} locale="en" />);
    expect(
      screen.queryByRole("link", { name: en.home.contests.tableOf.replace("{title}", "Spring round") }),
    ).toBeNull();
  });

  test("leads to the public table where there is one", () => {
    render(<RecentContests signedIn={false} contests={[running()]} dict={en} locale="en" />);
    // Named by the contest, not by the word: six rows carry this link, and
    // "Results, Results, Results" read out one after another names nothing.
    expect(
      screen.getByRole("link", { name: en.home.contests.tableOf.replace("{title}", "Spring round") }),
    ).toHaveAttribute(
      "href",
      `/contests/${running().id}/leaderboard`,
    );
  });

  /**
   * Three is the API's own ceiling, but a list that trusted it would print
   * however many cards a changed server sent onto a page whose whole argument
   * is that it is short.
   */
  test("shows at most three, and the freshest of them", () => {
    const many = Array.from({ length: 9 }, (_, index) =>
      running({ id: `contest-${index}`, title: `Round ${index}` }),
    );
    render(<RecentContests signedIn={false} contests={many} dict={en} locale="en" />);

    const rows = screen.getAllByRole("listitem");
    expect(rows).toHaveLength(3);
    expect(rows[0]).toHaveTextContent("Round 0");
    expect(screen.queryByText("Round 3")).toBeNull();
  });

  test("marks the one that is on right now, and says what each state is", () => {
    render(
      <RecentContests
        signedIn={false}
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
   * The card's rendition, at the address that carries the hash.
   *
   * The hash is what makes a replaced cover appear: the path names the
   * contest rather than the file, so an address without it is the address of
   * the old picture, and the API answers it with a year of caching.
   */
  test("shows the picture an organiser uploaded", () => {
    render(
      <RecentContests
        signedIn={false}
        contests={[running({ coverHash: "9f86d081884c7d65", coverAttribution: "Photo: A. Organiser, CC BY 4.0" })]}
        dict={en}
        locale="en"
      />,
    );

    const picture = screen.getByRole("img", {
      name: en.home.contests.coverOf.replace("{title}", "Spring round"),
    });
    expect(picture).toHaveAttribute(
      "src",
      `/api/v1/public/contests/${running().id}/cover?size=800&v=9f86d081884c7d65`,
    );
    // Against a page that jumps as six pictures arrive, and against loading
    // six of them for a reader who never scrolls that far.
    expect(picture).toHaveAttribute("loading", "lazy");
    expect(picture).toHaveAttribute("width");
    expect(picture).toHaveAttribute("height");
  });

  /**
   * A contest nobody uploaded a picture for gets a cover of its own, not a
   * grey rectangle. On the day an installation opens that is every contest on
   * the page, and a grid of empty frames would be the first thing a visitor
   * saw.
   */
  test("draws a cover for a contest that has no picture", () => {
    render(<RecentContests signedIn={false} contests={[running()]} dict={en} locale="en" />);

    expect(card().querySelector("img")).toBeNull();
    // The drawn cover's own geometry. What it draws is its test's business;
    // what matters here is that the frame is not left empty.
    expect(card().querySelector("svg")).not.toBeNull();
  });

  /**
   * The title is never laid on the photograph itself. It sits on a scrim that
   * resolves to the page's ground (design spec §10.2), which is what
   * guarantees its contrast whatever the organiser's picture happens to have
   * in its bottom third — and the drawn cover carries the same one, so a
   * mixed row reads as one row.
   */
  test.each([
    ["an uploaded cover", { coverHash: "9f86d081884c7d65", coverAttribution: "Photo: A. Organiser" }],
    ["a drawn cover", {}],
  ])("puts the title of a card with %s on a scrim", (_name, over) => {
    render(<RecentContests signedIn={false} contests={[running(over)]} dict={en} locale="en" />);

    expect(card().querySelector(".from-scrim-a")).not.toBeNull();
    expect(card()).toHaveTextContent("Spring round");
  });

  /**
   * An uploaded picture is somebody's work and is not published without the
   * line saying whose (design spec §10.1). A drawn cover has none to carry,
   * because its author is us.
   */
  test("credits an uploaded picture, and only an uploaded one", () => {
    const credited = render(
      <RecentContests
        signedIn={false}
        contests={[running({ coverHash: "9f86d081884c7d65", coverAttribution: "Photo: A. Organiser, CC BY 4.0" })]}
        dict={en}
        locale="en"
      />,
    );
    expect(screen.getByText("Photo: A. Organiser, CC BY 4.0")).toBeInTheDocument();
    credited.unmount();

    render(<RecentContests signedIn={false} contests={[running()]} dict={en} locale="en" />);
    expect(screen.queryByText(/Photo:/)).toBeNull();
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
    render(<RecentContests signedIn={false} contests={contests} dict={en} locale="en" />);

    expect(screen.getByText(en.home.contests.empty.body)).toBeInTheDocument();
    // Signed out, the label names the door the visitor actually meets: the
    // catalogue is behind sign-in, and `/open` carries them through it.
    expect(
      screen.getByRole("link", { name: en.home.contests.empty.actionSignedOut }),
    ).toHaveAttribute(
      "href",
      "/open",
    );
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
  });

  /** The hero's second action points here, so the section has to be here. */
  test("carries the anchor the hero points at", () => {
    const { container } = render(<RecentContests signedIn={false} contests={[running()]} dict={en} locale="en" />);
    expect(container.querySelector("#contests")).not.toBeNull();
  });
});

// The same empty state, read by somebody who is signed in: the catalogue is
// a catalogue to them, and saying "sign in" to a signed-in reader is the kind
// of sentence that makes a product look like it is not paying attention.
test("offers a signed-in reader the catalogue by its own name", () => {
  render(<RecentContests signedIn contests={[]} dict={en} locale="en" />);

  expect(screen.getByRole("link", { name: en.home.contests.empty.action })).toHaveAttribute(
    "href",
    "/open",
  );
});
