import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { PaneSwitch } from "./pane-switch";

const t = en.participant.play.workspace.phone;

function show(props: Partial<Parameters<typeof PaneSwitch>[0]> = {}) {
  const onChange = vi.fn();
  const view = render(<PaneSwitch value="editor" onChange={onChange} schema marked={null} dict={en} {...props} />);
  return { ...view, onChange };
}

describe("the pane switch", () => {
  test("is a named radio group of four parts, with the shown pane checked", () => {
    show({ value: "result" });

    const group = screen.getByRole("radiogroup", { name: t.label });
    expect(group).toBeInTheDocument();
    expect(screen.getAllByRole("radio").map((radio) => radio.getAttribute("value"))).toEqual([
      "editor",
      "result",
      "schema",
      "side",
    ]);
    expect(screen.getByRole("radio", { name: t.editor })).not.toBeChecked();
    expect(screen.getByRole("radio", { name: t.result })).toBeChecked();
    expect(screen.getByRole("radio", { name: t.schema })).not.toBeChecked();
    expect(screen.getByRole("radio", { name: t.side })).not.toBeChecked();
  });

  test("has no schema part in a contest that hides its schema", () => {
    show({ schema: false });

    expect(screen.getAllByRole("radio")).toHaveLength(3);
    expect(screen.queryByRole("radio", { name: t.schema })).not.toBeInTheDocument();
  });

  test("a tap asks for the pane it names", async () => {
    const { onChange } = show();

    await userEvent.click(screen.getByRole("radio", { name: t.side }));

    expect(onChange).toHaveBeenCalledWith("side");
  });

  test("an arrow key asks for the next pane, as in any radio group", async () => {
    const { onChange } = show();

    screen.getByRole("radio", { name: t.editor }).focus();
    await userEvent.keyboard("{ArrowRight}");

    expect(onChange).toHaveBeenCalledWith("result");
  });

  // jsdom matches no media query, so the classes are the contract here; the
  // boxes are measured in a browser at 375px.
  test("is drawn only below the narrow breakpoint, stuck under the bar, each part 44px high", () => {
    show();

    const group = screen.getByRole("radiogroup", { name: t.label });
    expect(group).toHaveClass("narrow:hidden", "sticky", "top-[calc(3rem+1px)]");
    for (const radio of screen.getAllByRole("radio")) {
      expect(radio.closest("label")).toHaveClass("min-h-11");
    }
  });

  test("says in words, on the questions part, that something new waits there", () => {
    show({ marked: "Something new from the organisers" });

    expect(screen.getByRole("radio", { name: t.side })).toHaveAccessibleDescription(
      "Something new from the organisers",
    );
    expect(screen.getByTestId("pane-switch-marker")).toBeInTheDocument();
  });

  test("draws no marker when nothing waits", () => {
    show();

    expect(screen.queryByTestId("pane-switch-marker")).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: t.side })).not.toHaveAccessibleDescription();
  });
});
