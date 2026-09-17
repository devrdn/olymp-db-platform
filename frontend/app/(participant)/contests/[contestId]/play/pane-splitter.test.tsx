import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

// The workspace is rendered whole, because a divider only means anything
// between two panes. Its collaborators are stubbed to nothing — this file is
// about the widths, and each of them is tested where it lives.
vi.mock("./actions", () => ({
  runQueryAction: async () => ({ kind: "idle" }),
  submitAnswerAction: async () => ({ kind: "idle" }),
  refreshQuestionsAction: async () => ({ kind: "ok", items: [] }),
  fetchQueryLogAction: async () => ({ kind: "ok", items: [], total: 0 }),
}));
vi.mock("./use-contest-events", () => ({
  useContestEvents: () => ({ offsetRef: { current: 0 }, deadlineRef: { current: null }, phase: "running" }),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: vi.fn() }) }));

import { Workspace } from "./workspace";
import { DEFAULT_SCHEMA_REM, DEFAULT_SIDE_REM } from "./pane-splitter";

const A_SCHEMA = {
  truncated: false,
  tables: [{ name: "guests", columns: [{ name: "id", type: "uuid", nullable: false, references: "" }] }],
};

function show(contestId = "c1") {
  return render(
    <Workspace
      contestId={contestId}
      storyBody={<p>A body in the stacks.</p>}
      // This file is about the widths of the interactive panes; the print
      // copy is rendered on the server now (page.tsx) and has none.
      printView={null}
      storyUnavailable={null}
      questionEntries={[]}
      schema={A_SCHEMA}
      initialLog={{ items: [], total: 0, failed: false }}
      workspace={null}
      locale="en"
      dict={en}
    />,
  );
}

afterEach(() => {
  window.localStorage.clear();
});

/**
 * SPEC.md §11 asks `ConsoleShell` for "three panes with resizable, remembered
 * sizes", and the reason is the one a fixed layout ran into: how much room
 * the questions need is a property of the contest, not of the product. An
 * olympiad whose questions are two lines and one whose questions are a
 * paragraph want different columns.
 */
describe("the console's panes", () => {
  const t = en.participant.play.workspace.panes;

  test("can be moved with the keyboard, not only with a pointer", async () => {
    const user = userEvent.setup();
    const { container } = show();

    const handle = screen.getByRole("separator", { name: t.side });
    expect(handle).toHaveAttribute("aria-valuenow", String(Math.round(DEFAULT_SIDE_REM)));

    handle.focus();
    await user.keyboard("{ArrowLeft}");

    // Left widens the questions: the pane is on the right, so its edge moves
    // against the pointer.
    expect(handle).toHaveAttribute("aria-valuenow", String(Math.round(DEFAULT_SIDE_REM) + 1));
    expect(container.querySelector<HTMLElement>("[style*='--pane-side']")?.style.getPropertyValue("--pane-side")).toBe(
      `${DEFAULT_SIDE_REM + 1}rem`,
    );
  });

  // The divider is a 1px hairline on purpose — the pane is the thing, not
  // the handle — so what has to be big enough to press is the invisible area
  // around it. Measured, that area was 9px wide at every screen size, on a
  // control that first appears at 760px, which is a tablet somebody may well
  // be using with a finger. jsdom cannot measure a pseudo-element, so what is
  // held here is that the coarse-pointer widening is still declared.
  test("widens its grab area for a finger without stealing a mouse's clicks", () => {
    show();
    const handle = screen.getByRole("separator", { name: t.side });

    // The mouse-sized area: one pixel of hairline plus four either side.
    expect(handle.className).toMatch(/(^|\s)after:-left-1(\s|$)/);
    expect(handle.className).toMatch(/(^|\s)after:-right-1(\s|$)/);
    // The finger-sized one, and only where the pointer is coarse.
    expect(handle.className).toMatch(/(^|\s)pointer-coarse:after:-left-3(\s|$)/);
    expect(handle.className).toMatch(/(^|\s)pointer-coarse:after:-right-3(\s|$)/);
  });

  test("remembers a width across a visit, per contest", async () => {
    const user = userEvent.setup();
    const first = show("c1");

    screen.getByRole("separator", { name: t.side }).focus();
    await user.keyboard("{Shift>}{ArrowLeft}{/Shift}");
    first.unmount();

    show("c1");
    expect(screen.getByRole("separator", { name: t.side })).toHaveAttribute(
      "aria-valuenow",
      String(Math.round(DEFAULT_SIDE_REM) + 4),
    );

    // Another contest is another screen, and starts from the design's own
    // width rather than inheriting somebody else's question length.
    screen.getByRole("separator", { name: t.side }).blur();
    show("c2");
    const both = screen.getAllByRole("separator", { name: t.side });
    expect(both[both.length - 1]).toHaveAttribute("aria-valuenow", String(Math.round(DEFAULT_SIDE_REM)));
  });

  // A pane narrowed past this shows no column names and wraps every second
  // word; a participant who dragged too far in a hurry should not have to
  // drag back to read anything.
  test("cannot be collapsed to nothing", async () => {
    const user = userEvent.setup();
    show();

    const handle = screen.getByRole("separator", { name: t.schema });
    handle.focus();
    for (let i = 0; i < 40; i++) await user.keyboard("{ArrowLeft}");

    const value = Number(handle.getAttribute("aria-valuenow"));
    expect(value).toBeGreaterThanOrEqual(Number(handle.getAttribute("aria-valuemin")));
    expect(value).toBeGreaterThan(0);
  });

  test("starts from the widths the design draws", () => {
    show();

    expect(screen.getByRole("separator", { name: t.schema })).toHaveAttribute(
      "aria-valuenow",
      String(Math.round(DEFAULT_SCHEMA_REM)),
    );
  });
});
