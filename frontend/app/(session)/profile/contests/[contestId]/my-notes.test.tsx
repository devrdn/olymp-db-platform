import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { ProfileWorkspace } from "@/lib/api/profile";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { MyNotes } from "./my-notes";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const t = () => dict.profile.report.notes;

function renderTab(workspace: ProfileWorkspace) {
  return render(<MyNotes workspace={workspace} t={dict.profile.report} />);
}

describe("my notes", () => {
  test("shows the notes and every SQL tab as they were left", () => {
    renderTab({
      notes: { body: "suspects: 3", updatedAt: "2026-05-14T09:00:00Z" },
      tabs: [
        { id: "t1", title: "Query 1", position: 0, body: "SELECT 1", updatedAt: "2026-05-14T09:10:00Z" },
        { id: "t2", title: "Guests", position: 1, body: "SELECT * FROM guests", updatedAt: "2026-05-14T09:20:00Z" },
      ],
    });

    expect(screen.getByText("suspects: 3")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Query 1" })).toBeInTheDocument();
    // The SQL is highlighted, so it is read from the block rather than as
    // one run of text: the keywords are elements of their own.
    const blocks = [...document.querySelectorAll("pre code")].map((code) => code.textContent);
    expect(blocks).toContain("SELECT * FROM guests");
    expect(screen.getByText(t().asLeft)).toBeInTheDocument();
  });

  /**
   * The history of an edit is a monitoring fact about how somebody worked,
   * and it is the organiser's tool rather than a record the participant is
   * handed back (design §2.2).
   */
  test("offers no history of the edits", () => {
    renderTab({
      notes: { body: "suspects: 3", updatedAt: "2026-05-14T09:00:00Z" },
      tabs: [],
    });

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByText(t().noTabs)).toBeInTheDocument();
  });

  /**
   * The two columns split on the page's own breakpoint, not on a container
   * query: nothing in this screen's tree declares `@container`, so a
   * `@min-[…]` class here would never apply at any width.
   */
  test("splits on the breakpoint the rest of the screen uses", () => {
    renderTab({ notes: { body: "suspects: 3", updatedAt: null }, tabs: [] });

    const columns = document.querySelector("[data-columns]") as HTMLElement;
    expect(columns).toHaveClass("max-narrow:grid-cols-1");
    expect(columns.className).not.toMatch(/@/);
  });

  test("says so when there is neither a note nor a tab", () => {
    renderTab({ notes: { body: "", updatedAt: null }, tabs: [] });

    expect(screen.getByText(t().empty)).toBeInTheDocument();
    expect(screen.queryByText(t().emptyNotes)).not.toBeInTheDocument();
  });
});
