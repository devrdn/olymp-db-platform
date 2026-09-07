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
