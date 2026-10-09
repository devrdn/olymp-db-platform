import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

// The whole workspace is rendered, since a divider needs two panes; its
// collaborators are stubbed out and tested where they live.
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
      accountId="u1"
      contestId={contestId}
      storyBody={<p>A body in the stacks.</p>}
      // Rendered on the server in production; it has no panes.
      printView={null}
      storyCover={null}
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

/** SPEC.md §11: three panes with resizable, remembered sizes. */
describe("the console's panes", () => {
  const t = en.participant.play.workspace.panes;

  test("can be moved with the keyboard, not only with a pointer", async () => {
    const user = userEvent.setup();
    const { container } = show();

    const handle = screen.getByRole("separator", { name: t.side });
    expect(handle).toHaveAttribute("aria-valuenow", String(Math.round(DEFAULT_SIDE_REM)));

    handle.focus();
    await user.keyboard("{ArrowLeft}");

    // Left widens the questions: the pane is on the right.
    expect(handle).toHaveAttribute("aria-valuenow", String(Math.round(DEFAULT_SIDE_REM) + 1));
    expect(container.querySelector<HTMLElement>("[style*='--pane-side']")?.style.getPropertyValue("--pane-side")).toBe(
      `${DEFAULT_SIDE_REM + 1}rem`,
    );
  });

  // The hairline's grab area is 9px for a mouse and wider for a finger on
  // the tablets that see it from 760px. jsdom cannot measure a
  // pseudo-element, so the declared classes are checked.
  test("widens its grab area for a finger without stealing a mouse's clicks", () => {
    show();
    const handle = screen.getByRole("separator", { name: t.side });

    // Mouse-sized: one pixel of hairline plus four either side.
    expect(handle.className).toMatch(/(^|\s)after:-left-1(\s|$)/);
    expect(handle.className).toMatch(/(^|\s)after:-right-1(\s|$)/);
    // Finger-sized, only where the pointer is coarse.
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

    // Another contest starts from the design's width.
    screen.getByRole("separator", { name: t.side }).blur();
    show("c2");
    const both = screen.getAllByRole("separator", { name: t.side });
    expect(both[both.length - 1]).toHaveAttribute("aria-valuenow", String(Math.round(DEFAULT_SIDE_REM)));
  });

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
 * SPEC.md §5: the editor's split is draggable too, stored as a share of the column's
 * height (the viewport's), since a stored rem would differ per screen.
 */
describe("the edge between the editor and the panel below it", () => {
  const t = en.participant.play.workspace.panes;

  test("is a horizontal separator that starts from the design's own share", () => {
    show();

    const handle = screen.getByRole("separator", { name: t.editor });
    expect(handle).toHaveAttribute("aria-orientation", "horizontal");
    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT));
    // The grab area widens vertically, and the handle is absent below the
    // breakpoint, where the column's height is its content.
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

  // A drag writes straight onto the column and commits on release; on this
  // axis the share is read against the column's measured height.
  test("a drag reads the column's own height, and commits once on release", () => {
    const { container } = show();
    const handle = screen.getByRole("separator", { name: t.editor });
    const column = container.querySelector<HTMLElement>("[style*='--pane-editor']") as HTMLElement;
    column.getBoundingClientRect = () => ({ height: 400, width: 800, top: 0, left: 0, right: 800, bottom: 400, x: 0, y: 0, toJSON: () => ({}) });

    handle.setPointerCapture = () => {};
    fireEvent.pointerDown(handle, { pointerId: 1, clientX: 0, clientY: 200 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 0, clientY: 240 });

    // 40px of a 400px column is ten points of share, on the element, not in
    // state.
    expect(column.style.getPropertyValue("--pane-editor")).toBe(`${DEFAULT_EDITOR_PCT + 10}%`);
    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT));

    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 0, clientY: 240 });
    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT + 10));
  });

  // A touch drag can end without a pointerup (a second finger, a scroll
  // takeover), which would leave a drag nothing ends and an uncommitted size.
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

  // Losing capture is the same interruption; after a normal release there is
  // no drag left, so it cannot undo a committed size.
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

    // A browser's real order: pointerup, then the implicit capture loss.
    fireEvent.pointerDown(handle, { pointerId: 2, clientX: 0, clientY: 200 });
    fireEvent.pointerMove(handle, { pointerId: 2, clientX: 0, clientY: 240 });
    fireEvent.pointerUp(handle, { pointerId: 2, clientX: 0, clientY: 240 });
    fireEvent.lostPointerCapture(handle, { pointerId: 2 });

    expect(handle).toHaveAttribute("aria-valuenow", String(DEFAULT_EDITOR_PCT + 10));
  });
});
