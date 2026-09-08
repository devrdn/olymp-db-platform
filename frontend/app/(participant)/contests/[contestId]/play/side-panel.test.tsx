import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

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

  // Printing happens on this screen now, not on a separate route (this
  // file's own doc explains why) — a button that opens the browser's own
  // print dialog in place, not a link that navigates anywhere.
  describe("the print control", () => {
    // Captured inside the mock itself, not read afterward — `window.print`
    // is a no-op in jsdom either way, so the only way to prove the title was
    // set *before* the call (not merely by the time the click handler
    // returns, which a same-tick reordering would also satisfy) is to read
    // `document.title` from inside the very call the mock stands in for.
    let titleAtPrintTime: string | undefined;

    beforeEach(() => {
      titleAtPrintTime = undefined;
      // jsdom has no printing pipeline of its own — see print-button.tsx's
      // own test for the same reasoning. This proves what jsdom can prove:
      // that a print was asked for, and when the document's title carried
      // the filename a browser's "Save as PDF" reads it from.
      vi.spyOn(window, "print").mockImplementation(() => {
        titleAtPrintTime = document.title;
      });
    });

    afterEach(() => {
      vi.restoreAllMocks();
    });

    test("is a button, not a link — nothing here navigates away", () => {
      show();

      const button = screen.getByRole("button", { name: en.participant.play.workspace.story.print, hidden: true });
      expect(button).toHaveAttribute("type", "button");
      expect(screen.queryByRole("link", { name: en.participant.play.workspace.story.print })).not.toBeInTheDocument();
    });

    // Chrome, Edge and Safari all suggest `document.title` as the filename
    // for "Save as PDF" — this is the whole mechanism by which the saved
    // file ends up named `story-{contestId}.pdf` instead of whatever this
    // tab happened to be called.
    test("names the file after the contest, prints, and puts the tab's own title back afterward", async () => {
      document.title = "DBContest";
      show();

      await userEvent.click(
        screen.getByRole("button", { name: en.participant.play.workspace.story.print, hidden: true }),
      );

      // Proves the ordering the task requires: the title carried
      // `story-c1` at the exact moment `window.print()` ran, not merely
      // at some point before or after it.
      expect(titleAtPrintTime).toBe("story-c1");
      expect(document.title).toBe("story-c1");
      expect(window.print).toHaveBeenCalledTimes(1);

      // Chrome and Firefox fire this once the print dialog closes.
      window.dispatchEvent(new Event("afterprint"));

      expect(document.title).toBe("DBContest");
    });

    // The defect the task exists to rule out: a participant's tab titled
    // `story-…` for the rest of their olympiad. `afterprint` does not fire
    // everywhere (older Safari, some in-app webviews), so the title has to
    // come back on its own even then.
    test("restores the title even on a browser that never fires afterprint", () => {
      vi.useFakeTimers();
      document.title = "DBContest";
      show();

      fireEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.story.print, hidden: true }));
      expect(document.title).toBe("story-c1");

      // No `afterprint` dispatched here — only time passing.
      vi.runOnlyPendingTimers();

      expect(document.title).toBe("DBContest");
      vi.useRealTimers();
    });

    test("does not restore the title twice, once from afterprint and once from the defensive timer", () => {
      vi.useFakeTimers();
      document.title = "DBContest";
      show();

      fireEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.story.print, hidden: true }));
      window.dispatchEvent(new Event("afterprint"));
      expect(document.title).toBe("DBContest");

      // A later, unrelated title change must survive the defensive timer
      // firing after `afterprint` already restored it once.
      document.title = "Something else entirely";
      vi.runOnlyPendingTimers();

      expect(document.title).toBe("Something else entirely");
      vi.useRealTimers();
    });
  });

  test("offers neither the download nor the print control when there is no story to take away", () => {
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
      screen.queryByRole("button", { name: en.participant.play.workspace.story.print, hidden: true }),
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
