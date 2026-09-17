import { fireEvent, render, screen } from "@testing-library/react";
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
import { DEFAULT_EDITOR_PCT, DEFAULT_SCHEMA_REM, DEFAULT_SIDE_REM } from "./pane-splitter";

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

/**
 * §7: the edge between the editor and the panel below it is draggable too.
 * It used to be a fixed 11:9, which is a share of the screen the product
 * picked — and how much of it a participant wants for the answer is theirs,
 * not ours: reading a forty-column row and writing a fifteen-line query want
 * opposite splits.
 *
 * The same handle as the two vertical edges, turned a quarter: a share of the
 * column's height rather than a width in rem, because the column's height is
 * the viewport's and a stored rem would mean something different on every
 * screen the contest is sat in front of.
 */
describe("the edge between the editor and the panel below it", () => {
  const t = en.participant.play.workspace.panes;

  test("is a horizontal separator that starts from the design's own share", () => {
    show();

    const handle = screen.getByRole("separator", { name: t.editor });
    expect(handle).toHaveAttribute("aria-orientation", "horizontal");
    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT));
    // Its grab area widens above and below rather than left and right, and
    // it is absent below the breakpoint, where the column's height is its own
    // content and a share of it means nothing.
    expect(handle.className).toMatch(/(^|\s)after:-top-1(\s|$)/);
    expect(handle.className).toMatch(/(^|\s)pointer-coarse:after:-bottom-3(\s|$)/);
    expect(handle.className).toMatch(/(^|\s)max-narrow:hidden(\s|$)/);
  });

  test("moves with the arrow keys, and drives the column's own track", async () => {
    const user = userEvent.setup();
    const { container } = show();

    const handle = screen.getByRole("separator", { name: t.editor });
    handle.focus();
    await user.keyboard("{ArrowDown}");

    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT + 1));
    expect(
      container.querySelector<HTMLElement>("[style*='--pane-editor']")?.style.getPropertyValue("--pane-editor"),
    ).toBe(`${DEFAULT_EDITOR_PCT + 1}%`);
  });

  test("remembers its share across a visit, per contest", async () => {
    const user = userEvent.setup();
    const first = show("c1");

    screen.getByRole("separator", { name: t.editor }).focus();
    await user.keyboard("{Shift>}{ArrowUp}{/Shift}");
    first.unmount();

    show("c1");
    expect(screen.getByRole("separator", { name: t.editor })).toHaveAttribute(
      "aria-valuenow",
      String(DEFAULT_EDITOR_PCT - 4),
    );
  });

  test("leaves both panes something to be, however far it is pushed", async () => {
    const user = userEvent.setup();
    show();

    const handle = screen.getByRole("separator", { name: t.editor });
    handle.focus();
    for (let i = 0; i < 120; i++) await user.keyboard("{ArrowUp}");

    expect(Number(handle.getAttribute("aria-valuenow"))).toBe(
      Number(handle.getAttribute("aria-valuemin")),
    );
    expect(Number(handle.getAttribute("aria-valuemin"))).toBeGreaterThan(0);
  });

  // A pointer drag never goes through React (see the module's own doc): it
  // writes the share straight onto the column, and state is written once on
  // release. What makes that work on this axis is that the share is read
  // against the column's measured height, not against a font size.
  test("a drag reads the column's own height, and commits once on release", () => {
    const { container } = show();
    const handle = screen.getByRole("separator", { name: t.editor });
    const column = container.querySelector<HTMLElement>("[style*='--pane-editor']") as HTMLElement;
    column.getBoundingClientRect = () => ({ height: 400, width: 800, top: 0, left: 0, right: 800, bottom: 400, x: 0, y: 0, toJSON: () => ({}) });

    handle.setPointerCapture = () => {};
    fireEvent.pointerDown(handle, { pointerId: 1, clientX: 0, clientY: 200 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 0, clientY: 240 });

    // Forty pixels of a four-hundred-pixel column is ten points of share, and
    // it is on the element rather than in state.
    expect(column.style.getPropertyValue("--pane-editor")).toBe(`${DEFAULT_EDITOR_PCT + 10}%`);
    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT));

    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 0, clientY: 240 });
    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT + 10));
  });

  // A touch drag ends in ways a mouse drag does not: a second finger, the
  // browser taking the gesture over as a scroll, a call arriving. The
  // pointer then never comes up. Left alone, the handle stays in a drag
  // nothing will ever end — the next pointer to cross it moves the edge
  // with nothing pressed — and the column keeps a size nobody committed.
  test("an interrupted drag ends, and leaves the column where it started", () => {
    const { container } = show();
    const handle = screen.getByRole("separator", { name: t.editor });
    const column = container.querySelector<HTMLElement>("[style*='--pane-editor']") as HTMLElement;
    column.getBoundingClientRect = () => ({ height: 400, width: 800, top: 0, left: 0, right: 800, bottom: 400, x: 0, y: 0, toJSON: () => ({}) });

    handle.setPointerCapture = () => {};
    fireEvent.pointerDown(handle, { pointerId: 1, clientX: 0, clientY: 200 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 0, clientY: 240 });
    fireEvent.pointerCancel(handle, { pointerId: 1 });

    expect(column.style.getPropertyValue("--pane-editor")).toBe(`${DEFAULT_EDITOR_PCT}%`);
    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT));

    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 0, clientY: 320 });
    expect(column.style.getPropertyValue("--pane-editor")).toBe(`${DEFAULT_EDITOR_PCT}%`);
  });

  // The capture can be lost on its own — an element removed, a browser that
  // decides the gesture belongs to it — and that is the same interruption
  // by another name. After an ordinary release there is no drag left for it
  // to undo, which is what keeps it from taking back a size just committed.
  test("a lost pointer capture ends the drag too, and never undoes a release", () => {
    const { container } = show();
    const handle = screen.getByRole("separator", { name: t.editor });
    const column = container.querySelector<HTMLElement>("[style*='--pane-editor']") as HTMLElement;
    column.getBoundingClientRect = () => ({ height: 400, width: 800, top: 0, left: 0, right: 800, bottom: 400, x: 0, y: 0, toJSON: () => ({}) });

    handle.setPointerCapture = () => {};
    fireEvent.pointerDown(handle, { pointerId: 1, clientX: 0, clientY: 200 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 0, clientY: 240 });
    fireEvent.lostPointerCapture(handle, { pointerId: 1 });

    expect(column.style.getPropertyValue("--pane-editor")).toBe(`${DEFAULT_EDITOR_PCT}%`);

    // The release order a browser really uses: pointerup, then the implicit
    // loss of the capture. The share committed on the way up stands.
    fireEvent.pointerDown(handle, { pointerId: 2, clientX: 0, clientY: 200 });
    fireEvent.pointerMove(handle, { pointerId: 2, clientX: 0, clientY: 240 });
    fireEvent.pointerUp(handle, { pointerId: 2, clientX: 0, clientY: 240 });
    fireEvent.lostPointerCapture(handle, { pointerId: 2 });

    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT + 10));
  });
});
