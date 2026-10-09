import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { PublicContest } from "@/lib/api/showcase";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { RecentContests } from "./recent-contests";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

/** A running contest; tests override the field they care about. */
function running(over: Partial<PublicContest> = {}): PublicContest {
  return {
    id: "01JB0000000000000000000001",
    title: "Spring round",
    status: "running",
    startsAt: "2026-03-14T08:00:00Z",
    endsAt: "2026-03-14T11:00:00Z",
    tableOpen: true,
    // No picture by default, as on a fresh installation.
    coverHash: undefined,
    coverAttribution: undefined,
    ...over,
  };
}

/** The card of the one contest rendered. */
function card(): HTMLElement {
  return screen.getAllByRole("listitem")[0];
}

describe("the contest list", () => {
  /** The table link appears only when the table is open. */
  test("leads to the public table only where there is one to read", () => {
    render(<RecentContests signedIn={false} contests={[running({ tableOpen: false })]} dict={en} locale="en" />);
    expect(
      screen.queryByRole("link", { name: en.home.contests.tableOf.replace("{title}", "Spring round") }),
    ).toBeNull();
  });

  test("leads to the public table where there is one", () => {
    render(<RecentContests signedIn={false} contests={[running()]} dict={en} locale="en" />);
    // Named by the contest, so repeated links are distinguishable.
    expect(
      screen.getByRole("link", { name: en.home.contests.tableOf.replace("{title}", "Spring round") }),
    ).toHaveAttribute(
      "href",
      `/contests/${running().id}/leaderboard`,
    );
  });

  /** The page caps at three even if the server sends more. */
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
    // The accent dot marks only what is running now.
    expect(rows[0].querySelectorAll(".bg-accent")).toHaveLength(1);
    expect(rows[1].querySelectorAll(".bg-accent")).toHaveLength(0);
  });

  /** The address carries the hash, or a replaced cover would be served from a year-long cache. */
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
    // Lazy, so pictures below the fold are not loaded.
    expect(picture).toHaveAttribute("loading", "lazy");
    expect(picture).toHaveAttribute("width");
    expect(picture).toHaveAttribute("height");
  });

  /** No upload means a drawn cover, not an empty frame. */
  test("draws a cover for a contest that has no picture", () => {
    render(<RecentContests signedIn={false} contests={[running()]} dict={en} locale="en" />);

    expect(card().querySelector("img")).toBeNull();
    // The frame is not empty; the drawing has its own tests.
    expect(card().querySelector("svg")).not.toBeNull();
  });

  /** The title sits on a scrim (SPEC.md §10.2) on both uploaded and drawn covers. */
  test.each([
    ["an uploaded cover", { coverHash: "9f86d081884c7d65", coverAttribution: "Photo: A. Organiser" }],
    ["a drawn cover", {}],
  ])("puts the title of a card with %s on a scrim", (_name, over) => {
    render(<RecentContests signedIn={false} contests={[running(over)]} dict={en} locale="en" />);

    expect(card().querySelector(".from-scrim-a")).not.toBeNull();
    expect(card()).toHaveTextContent("Spring round");
  });

  /** Uploaded pictures are credited (SPEC.md §10.1); drawn covers are not. */
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

  /** Empty and failed reads share one state, which must offer a next step. */
  test.each([
    ["nothing to show", [] as PublicContest[]],
    ["a failed read", null],
  ])("explains %s and names the way on, rather than ruling an empty table", (_name, contests) => {
    render(<RecentContests signedIn={false} contests={contests} dict={en} locale="en" />);

    expect(screen.getByText(en.home.contests.empty.body)).toBeInTheDocument();
    // Signed out, the label names the sign-in door.
    expect(
      screen.getByRole("link", { name: en.home.contests.empty.actionSignedOut }),
    ).toHaveAttribute(
      "href",
      "/open",
    );
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
  });

  /** The hero links to this anchor. */
  test("carries the anchor the hero points at", () => {
    const { container } = render(<RecentContests signedIn={false} contests={[running()]} dict={en} locale="en" />);
    expect(container.querySelector("#contests")).not.toBeNull();
  });
});

// Signed in, the empty state names the catalogue, not sign-in.
test("offers a signed-in reader the catalogue by its own name", () => {
  render(<RecentContests signedIn contests={[]} dict={en} locale="en" />);

  expect(screen.getByRole("link", { name: en.home.contests.empty.action })).toHaveAttribute(
    "href",
    "/open",
  );
});
