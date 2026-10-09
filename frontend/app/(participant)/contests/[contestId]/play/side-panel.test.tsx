import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { fetchStandingsAction } from "./actions";
import { SidePanel } from "./side-panel";

vi.mock("./actions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./actions")>()),
  fetchStandingsAction: vi.fn(),
}));

function show() {
  return render(
    <SidePanel
      accountId="u1"
      storyBody={<p>A body in the stacks.</p>}
      storyCover={null}
      storyUnavailable={null}
      contestId="c1"
      questionEntries={[]}
      initialNotes={{ body: "the butler did it", updatedAt: "2026-09-17T10:00:00Z" }}
      dict={en}
      locale="en"
    />,
  );
}

describe("the side panel", () => {
  test("shows both tabs", () => {
    show();

    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.story })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.questions })).toBeInTheDocument();
  });

  test("offers the notes as the tab right after the questions", () => {
    show();

    const names = screen.getAllByRole("tab").map((tab) => tab.textContent);
    const questions = names.indexOf(en.participant.play.workspace.tabs.questions);
    expect(names[questions + 1]).toBe(en.participant.play.workspace.tabs.notes);
  });

  test("keeps the notes mounted behind the other tabs, so their autosave keeps running", () => {
    show();

    expect(
      screen.getByRole("textbox", { name: en.participant.play.workspace.notes.label, hidden: true }),
    ).toHaveValue("the butler did it");
  });

  test("shows the notes field once its tab is chosen", async () => {
    show();

    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.notes }));

    expect(screen.getByRole("textbox", { name: en.participant.play.workspace.notes.label })).toBeVisible();
  });

  // Both panels are in the DOM from the start, so the story's text is
  // found without selecting its tab.
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
        accountId="u1"
        storyBody={null}
        storyCover={null}
        storyUnavailable="This contest has no story yet"
        contestId="c1"
        questionEntries={[]}
        initialNotes={null}
        dict={en}
        locale="en"
      />,
    );

    expect(screen.getByText("This contest has no story yet")).toBeInTheDocument();
  });

  /** SPEC.md §10: the cover heads the story tab, before the export row and the prose. */
  test("heads the story with the contest's cover", () => {
    render(
      <SidePanel
        accountId="u1"
        storyBody={<p>A body in the stacks.</p>}
        storyCover={<p>The cover of The Warehouse Fire</p>}
        storyUnavailable={null}
        contestId="c1"
        questionEntries={[]}
        initialNotes={null}
        dict={en}
        locale="en"
      />,
    );

    const cover = screen.getByText("The cover of The Warehouse Fire");
    const story = screen.getByText("A body in the stacks.");
    // DOCUMENT_POSITION_FOLLOWING: the story comes after the cover.
    expect(cover.compareDocumentPosition(story) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  /** No story, no cover: the print control and the download follow the same rule. */
  test("shows no cover where there is no story for it to head", () => {
    render(
      <SidePanel
        accountId="u1"
        storyBody={null}
        storyCover={<p>The cover of The Warehouse Fire</p>}
        storyUnavailable="This contest has no story yet"
        contestId="c1"
        questionEntries={[]}
        initialNotes={null}
        dict={en}
        locale="en"
      />,
    );

    expect(screen.queryByText("The cover of The Warehouse Fire")).not.toBeInTheDocument();
  });

  test("offers the story as a Markdown download, as a link and not a button", () => {
    show();

    // `hidden: true`: the story tab is not showing, but stays in the DOM.
    const link = screen.getByRole("link", {
      name: en.participant.play.workspace.story.export.label,
      hidden: true,
    });
    expect(link).toHaveAttribute("href", "/contests/c1/play/story.md");
    expect(link).toHaveAttribute("download");
    expect(link).toHaveTextContent("Markdown");
  });

  // A button that opens the print dialog in place, not a link.
  describe("the print control", () => {
    // Read inside the mock, so the title is proven set before the call itself,
    // not merely by the time the handler returns.
    let titleAtPrintTime: string | undefined;

    beforeEach(() => {
      titleAtPrintTime = undefined;
      // jsdom has no printing; this proves a print was asked for and what the
      // title held at that moment.
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

    // Browsers suggest `document.title` as the "Save as PDF" filename.
    test("names the file after the contest, prints, and puts the tab's own title back afterward", async () => {
      document.title = "DBContest";
      show();

      await userEvent.click(
        screen.getByRole("button", { name: en.participant.play.workspace.story.print, hidden: true }),
      );

      expect(titleAtPrintTime).toBe("story-c1");
      expect(document.title).toBe("story-c1");
      expect(window.print).toHaveBeenCalledTimes(1);

      // Chrome and Firefox fire this once the print dialog closes.
      window.dispatchEvent(new Event("afterprint"));

      expect(document.title).toBe("DBContest");
    });

    // `afterprint` does not fire everywhere (older Safari, some webviews), and
    // the tab must not stay titled `story-…` for the rest of the olympiad.
    test("restores the title even on a browser that never fires afterprint", () => {
      vi.useFakeTimers();
      document.title = "DBContest";
      show();

      fireEvent.click(screen.getByRole("button", { name: en.participant.play.workspace.story.print, hidden: true }));
      expect(document.title).toBe("story-c1");

      // No `afterprint`, only time passing.
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

      // A later title change must survive the timer firing after `afterprint`
      // already restored the title.
      document.title = "Something else entirely";
      vi.runOnlyPendingTimers();

      expect(document.title).toBe("Something else entirely");
      vi.useRealTimers();
    });
  });

  test("offers neither the download nor the print control when there is no story to take away", () => {
    render(
      <SidePanel
        accountId="u1"
        storyBody={null}
        storyCover={null}
        storyUnavailable="This contest has no story yet"
        contestId="c1"
        questionEntries={[]}
        initialNotes={null}
        dict={en}
        locale="en"
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
 * jsdom lays nothing out, so this holds the rule rather than the size: a
 * scroll box containing `sr-only` text (`position: absolute`) must be a
 * containing block, or the hidden label escapes to the page and grows it.
 */
test("each scrolling panel is the containing block for the hidden labels inside it", () => {
  render(
    <SidePanel
      accountId="u1"
      storyBody={<p>A body in the stacks.</p>}
      storyCover={null}
      storyUnavailable={null}
      contestId="c1"
      questionEntries={[]}
      initialNotes={{ body: "", updatedAt: null }}
      dict={en}
      locale="en"
    />,
  );

  for (const panel of screen.getAllByRole("tabpanel", { hidden: true })) {
    expect(panel.className).toMatch(/(^|\s)overflow-y-auto(\s|$)/);
    expect(panel.className).toMatch(/(^|\s)relative(\s|$)/);
  }
});

/**
 * jsdom lays nothing out, so this checks the two classes that keep a long tab
 * strip from widening the page at any pane width: `overflow-x-auto` on the
 * tablist, and `min-w-0` on its flex container, which otherwise cannot shrink
 * below the labels' min-content width.
 */
test("keeps the tab strip from widening the page at any pane width", () => {
  render(
    <SidePanel
      accountId="u1"
      storyBody={<p>A body in the stacks.</p>}
      storyCover={null}
      storyUnavailable={null}
      contestId="c1"
      questionEntries={[]}
      initialNotes={{ body: "", updatedAt: null }}
      dict={en}
      locale="en"
    />,
  );

  const tablist = screen.getByRole("tablist");
  expect(tablist.className).toMatch(/(^|\s)overflow-x-auto(\s|$)/);
  expect(tablist.parentElement?.className).toMatch(/(^|\s)min-w-0(\s|$)/);
});

// The strip can be scrolled past the selected tab, so selecting one must
// bring it back into view.
test("scrolls the newly selected tab into view, since the strip may be scrolled past it", async () => {
  // jsdom has no `scrollIntoView` at all, so the property itself is mocked
  // and restored afterwards.
  const scrollIntoView = vi.fn();
  const original = HTMLElement.prototype.scrollIntoView;
  HTMLElement.prototype.scrollIntoView = scrollIntoView;
  try {
    show();

    await userEvent.click(screen.getByRole("tab", { name: en.participant.play.workspace.tabs.notes }));

    const notesTab = screen.getByRole("tab", { name: en.participant.play.workspace.tabs.notes });
    expect(scrollIntoView.mock.instances).toContain(notesTab);
  } finally {
    HTMLElement.prototype.scrollIntoView = original;
  }
});

describe("the side panel's table", () => {
  test("is read only once its tab is chosen, and shows whose row is whose", async () => {
    vi.mocked(fetchStandingsAction).mockResolvedValue({
      kind: "ok",
      standings: {
        state: "live", scoring: "points", title: "", generatedAt: "2026-09-20T10:00:00Z", truncated: false,
        frozenAt: undefined, endsAt: undefined, questions: undefined,
        rows: [{ place: 1, label: "sherlock", deleted: false, points: 12, solved: 2, penalty: undefined, cells: undefined, lastScoredAt: undefined, winner: false, isYou: true }],
      },
    });
    show();

    const tab = screen.getByRole("tab", { name: en.leaderboard.tab });
    expect(fetchStandingsAction).not.toHaveBeenCalled();

    await userEvent.click(tab);
    expect(await screen.findByText("sherlock")).toBeInTheDocument();
    expect(fetchStandingsAction).toHaveBeenCalledWith("c1");
    expect(screen.getByText(en.leaderboard.you)).toBeInTheDocument();
  });
});
