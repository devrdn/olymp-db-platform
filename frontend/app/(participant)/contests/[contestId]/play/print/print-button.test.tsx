import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { PrintButton } from "./print-button";

describe("PrintButton", () => {
  beforeEach(() => {
    // jsdom has no printing pipeline of its own; the browser's own dialog is
    // exactly what this control exists to trigger, so the test can only prove
    // that it asked for it.
    vi.spyOn(window, "print").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  test("asks the browser to print, on click", async () => {
    render(<PrintButton label="Print" />);

    await userEvent.click(screen.getByRole("button", { name: "Print" }));

    expect(window.print).toHaveBeenCalledTimes(1);
  });

  // The one piece of console furniture this route's own doc promises never to
  // print: the button that exists solely to open the print dialog would
  // otherwise appear on the printed page it produces.
  test("hides itself when the page it is on is printed", () => {
    render(<PrintButton label="Print" />);

    expect(screen.getByRole("button", { name: "Print" }).className).toMatch(/(^|\s)print:hidden(\s|$)/);
  });
});
