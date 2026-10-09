import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import { Tooltip } from "./tooltip";

const EXPLANATION = "Percent of this question's own points, lost for every wrong attempt.";

/** Behaviour only: jsdom has no layout, so viewport placement was checked in a browser. */
function Harness({ onOuterKeyDown }: { onOuterKeyDown?: (event: React.KeyboardEvent) => void }) {
  return (
    <div onKeyDown={onOuterKeyDown}>
      <button type="button">Before</button>
      <Tooltip label="Hint">{EXPLANATION}</Tooltip>
      <button type="button">After</button>
    </div>
  );
}

/** Escape presses that reached an outer handler (Tab presses reach it too). */
const escapesSeenBy = (handler: ReturnType<typeof vi.fn>) =>
  handler.mock.calls.filter(([event]) => (event as React.KeyboardEvent).key === "Escape").length;

const trigger = () => screen.getByRole("button", { name: "Hint" });
// A closed bubble is hidden but must still be found.
const bubble = () => screen.getByRole("tooltip", { hidden: true });

describe("Tooltip", () => {
  test("is a real button, named from the label rather than by the glyph", () => {
    render(<Harness />);

    expect(trigger()).toHaveAttribute("type", "button");
    expect(trigger()).toHaveAccessibleName("Hint");
  });

  test("describes the trigger with the explanation, even while closed", () => {
    render(<Harness />);

    expect(bubble()).not.toBeVisible();
    expect(trigger()).toHaveAttribute("aria-describedby", bubble().id);
    expect(trigger()).toHaveAccessibleDescription(EXPLANATION);
  });

  test("opens on hover, and closes once the pointer has left", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.hover(trigger());
    expect(bubble()).toBeVisible();

    await user.unhover(trigger());
    await waitFor(() => expect(bubble()).not.toBeVisible());
  });

  test("stays open while the pointer moves from the question mark onto the text", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.hover(trigger());
    await user.hover(bubble());
    // Longer than the leave grace.
    await new Promise((resolve) => setTimeout(resolve, 300));
    expect(bubble()).toBeVisible();

    await user.unhover(bubble());
    await waitFor(() => expect(bubble()).not.toBeVisible());
  });

  test("opens on a press alone, with no hover and no focus, and a second press closes it", () => {
    render(<Harness />);

    fireEvent.click(trigger());
    expect(bubble()).toBeVisible();

    fireEvent.click(trigger());
    expect(bubble()).not.toBeVisible();
  });

  test("a tap opens it and leaves it open", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    // A touch press and release: pointerenter, pointerdown, focus, pointerup,
    // click, pointerleave — every one of them with pointerType "touch".
    await user.pointer({ keys: "[TouchA]", target: trigger() });
    await new Promise((resolve) => setTimeout(resolve, 300));

    expect(bubble()).toBeVisible();
  });

  test("does not take a finger passing over it for hover", () => {
    render(<Harness />);

    // React derives enter and leave from over and out. A touch must not open
    // the bubble; a mouse must.
    fireEvent.pointerOver(trigger(), { pointerType: "touch" });
    expect(bubble()).not.toBeVisible();

    fireEvent.pointerOver(trigger(), { pointerType: "mouse" });
    expect(bubble()).toBeVisible();
  });

  test("opens when the keyboard focuses it", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.tab(); // "Before"
    expect(bubble()).not.toBeVisible();

    await user.tab();
    expect(trigger()).toHaveFocus();
    expect(bubble()).toBeVisible();
  });

  test("closes on Escape, and the key goes no further than the bubble", async () => {
    const user = userEvent.setup();
    const outer = vi.fn();
    render(<Harness onOuterKeyDown={outer} />);

    await user.tab();
    await user.tab();
    expect(bubble()).toBeVisible();

    await user.keyboard("{Escape}");

    expect(bubble()).not.toBeVisible();
    // One press closes one layer.
    expect(escapesSeenBy(outer)).toBe(0);
  });

  test("lets Escape through when it is already closed", async () => {
    const user = userEvent.setup();
    const outer = vi.fn();
    render(<Harness onOuterKeyDown={outer} />);

    await user.click(screen.getByRole("button", { name: "Before" }));
    await user.keyboard("{Escape}");

    expect(escapesSeenBy(outer)).toBe(1);
  });

  test("closes on a press outside it", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    fireEvent.click(trigger());
    expect(bubble()).toBeVisible();

    await user.click(screen.getByRole("button", { name: "After" }));
    expect(bubble()).not.toBeVisible();
  });

  test("closes when focus moves away", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.tab();
    await user.tab();
    await user.keyboard("{Enter}"); // pinned by a press, as well as focused
    expect(bubble()).toBeVisible();

    await user.tab();
    expect(screen.getByRole("button", { name: "After" })).toHaveFocus();
    expect(bubble()).not.toBeVisible();
  });

  test("survives a press inside the bubble and the pointer leaving, so its text can be selected", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.hover(trigger());
    await user.pointer([{ keys: "[MouseLeft>]", target: bubble() }]);
    await user.unhover(bubble());
    await new Promise((resolve) => setTimeout(resolve, 300));

    expect(bubble()).toBeVisible();
  });
});
