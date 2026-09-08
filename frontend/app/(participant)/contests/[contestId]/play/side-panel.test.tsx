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

  // The last piece of export the plan asks for: the story as a file, beside
  // the story itself — the same placement query-log-panel.tsx already uses
  // for its own CSV link.
  test("offers the story as a Markdown download, as a link and not a button", () => {
    show();

    // `hidden: true`, the same as the "tabpanel" query above: the story tab
    // is not the one showing by default, and `Tabs` keeps it in the DOM
    // rather than unmounting it (this file's own doc), which is exactly what
    // makes it findable at all.
    const link = screen.getByRole("link", {
      name: en.participant.play.workspace.story.export.label,
      hidden: true,
    });
    expect(link).toHaveAttribute("href", "/contests/c1/play/story.md");
    expect(link).toHaveAttribute("download");
    expect(link).toHaveTextContent("Markdown");
  });

  // The print-ready view is a separate route (this file's own doc explains
  // why), so this is a plain navigation link rather than a member of
  // ExportMenu's own list — and it opens in a new tab so a participant
  // working under a timer never loses this screen to it, the same reasoning
  // StoryText's own citation links already follow.
  test("offers a way to the print-ready view, opened in a new tab", () => {
    show();

    const link = screen.getByRole("link", { name: en.participant.play.workspace.story.print, hidden: true });
    expect(link).toHaveAttribute("href", "/contests/c1/play/print");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
  });

  test("offers neither the download nor the print link when there is no story to take away", () => {
    render(
      <SidePanel
        storyBody={null}
        storyUnavailable="This contest has no story yet"
        contestId="c1"
        questionEntries={[]}
        dict={en}
      />,
    );

    expect(
      screen.queryByRole("link", { name: en.participant.play.workspace.story.export.label, hidden: true }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: en.participant.play.workspace.story.print, hidden: true }),
    ).not.toBeInTheDocument();
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
