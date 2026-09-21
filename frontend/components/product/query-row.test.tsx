import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { QueryRow } from "./query-row";
import { loggedQuery } from "./test-fixtures";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

/** The organiser's words and the address column: the fuller of the two rows. */
function renderRow(query = loggedQuery(1), address = true) {
  return render(
    <ol>
      <QueryRow
        query={query}
        labels={dict.workspace.monitor.participant.queries}
        statuses={dict.workspace.monitor.feed.queryStatus}
        locale="en"
        address={address}
      />
    </ol>,
  );
}

describe("one query", () => {
  test("says when, how it ended, how long it took, how many rows and from where", () => {
    renderRow(loggedQuery(1, { sql: "SELECT name\nFROM guests", durationMs: 42, rowCount: 7, ip: "10.0.0.5" }));

    expect(screen.getByText("ok")).toBeInTheDocument();
    expect(screen.getByText("42 ms")).toBeInTheDocument();
    expect(screen.getByText("7 rows")).toBeInTheDocument();
    expect(screen.getByText("10.0.0.5")).toBeInTheDocument();
    expect(screen.getByText("SELECT name")).toBeInTheDocument();
    expect(screen.getByRole("time")).toHaveAttribute("dateTime", "2026-09-20T10:14:03.120Z");
  });

  test("shows the error of one that failed, and says when no address was recorded", () => {
    renderRow(loggedQuery(1, { status: "error", error: "division by zero", durationMs: null, rowCount: null, ip: undefined }));

    expect(screen.getByText("error")).toBeInTheDocument();
    expect(screen.getByText("division by zero")).toBeInTheDocument();
    expect(screen.getByText("no address")).toBeInTheDocument();
    expect(screen.queryByText(/rows/)).not.toBeInTheDocument();
  });

  /**
   * A participant's own report shows the same row without the address: it is
   * their own, it explains nothing to them, and it is in the way (design
   * §2.2). Not even "no address" — there is no column to leave empty.
   */
  test("leaves the address out where it is not asked for", () => {
    renderRow(loggedQuery(1, { ip: "10.0.0.5" }), false);

    expect(screen.queryByText("10.0.0.5")).not.toBeInTheDocument();
    expect(screen.queryByText("no address")).not.toBeInTheDocument();
    expect(screen.getByText("ok")).toBeInTheDocument();
  });

  test("expands to the whole statement, highlighted and read-only", () => {
    renderRow(loggedQuery(1, { sql: "SELECT name\nFROM guests\nWHERE id = 4" }));
    const toggle = screen.getByRole("button", { name: "Show the query" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("code")).not.toBeInTheDocument();

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const code = document.querySelector("pre code") as HTMLElement;
    expect(code.textContent).toBe("SELECT name\nFROM guests\nWHERE id = 4");
    expect([...code.querySelectorAll("[data-token=keyword]")].map((el) => el.textContent)).toEqual(["SELECT", "FROM", "WHERE"]);
    expect(code.querySelector("[data-token=number]")?.textContent).toBe("4");
    expect(code.closest("[contenteditable]")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Hide the query" }));
    expect(document.querySelector("pre code")).toBeNull();
  });

  test("copies the whole statement and says so", async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    renderRow(loggedQuery(1, { sql: "SELECT 1\nFROM t" }));

    fireEvent.click(screen.getByRole("button", { name: "Show the query" }));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    });
    expect(writeText).toHaveBeenCalledWith("SELECT 1\nFROM t");
    expect(screen.getByRole("status")).toHaveTextContent("Copied");
  });

  test("says when copying failed", async () => {
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText: vi.fn(async () => Promise.reject(new Error("no"))) } });
    renderRow();

    fireEvent.click(screen.getByRole("button", { name: "Show the query" }));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    });
    expect(screen.getByRole("status")).toHaveTextContent("Could not copy");
  });

  test("says a shortened statement is shortened", () => {
    renderRow(loggedQuery(1, { sqlTruncated: true }));
    fireEvent.click(screen.getByRole("button", { name: "Show the query" }));
    expect(screen.getByText(dict.workspace.monitor.participant.queries.shortened)).toBeInTheDocument();
  });
});
