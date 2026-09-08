import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { SidePanel } from "./side-panel";

function show() {
  return render(
    <SidePanel
      storyBody={<p>A body in the stacks.</p>}
      storyUnavailable={null}
      contestId="c1"
      questionEntries={[]}
      dict={en}
    />,
  );
}

describe("the side panel", () => {
  test("shows both tabs", () => {
    show();

    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.questions })).toBeInTheDocument();
  });

  // Task 3's own requirement: switching a tab must not remount what is
  // behind it. Both panels are in the DOM from the start (`hidden` toggles,
  // nothing unmounts), which this proves by finding the story's text without
  // ever selecting its tab.
  test("keeps the story mounted while the questions tab is showing", () => {
    show();

    expect(screen.getByText("A body in the stacks.")).toBeInTheDocument();
  });

  test("switches which panel is visible on click", async () => {
    show();
    const storyTab = screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story });

    expect(storyTab).toHaveAttribute("aria-selected", "false");
    await userEvent.click(storyTab);
    expect(storyTab).toHaveAttribute("aria-selected", "true");
  });

  test("shows the reason instead of the story when it has none for this language", () => {
    render(
      <SidePanel
        storyBody={null}
        storyUnavailable="This contest has no story yet"
        contestId="c1"
        questionEntries={[]}
        dict={en}
      />,
    );

    expect(screen.getByText("This contest has no story yet")).toBeInTheDocument();
  });
});

/**
 * jsdom lays nothing out, so this cannot measure the 263px of empty page
 * scroll the defect produced (the numbers are in the commit message). What it
 * can hold is the rule the fix established: a scroll box that contains
 * `sr-only` text has to be a containing block, because `sr-only` is
 * `position: absolute` and a *static* scroll box does not clip one — the
 * hidden label then keeps the page's own coordinates and grows the document
 * with it.
 */
test("each scrolling panel is the containing block for the hidden labels inside it", () => {
  render(
    <SidePanel
      storyBody={<p>A body in the stacks.</p>}
      storyUnavailable={null}
      contestId="c1"
      questionEntries={[]}
      dict={en}
    />,
  );

  for (const panel of screen.getAllByRole("tabpanel", { hidden: true })) {
    expect(panel.className).toMatch(/(^|\s)overflow-y-auto(\s|$)/);
    expect(panel.className).toMatch(/(^|\s)relative(\s|$)/);
  }
});
