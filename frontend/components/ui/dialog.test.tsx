import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import { Dialog, DialogContent, DialogFooter, DialogTitle } from "./dialog";

/**
 * A small controlled harness: the real callers (`selection.tsx`'s
 * `ActionDialog`) hold `open` and `dismissible` in their own state and pass
 * both down, which is exactly what a caller of this component is expected to
 * do — `dismissible` is a prop of the dialog, not a fact it discovers on its
 * own.
 */
function Harness({ dismissible }: { dismissible: boolean }) {
  const [open, setOpen] = useState(true);
  return (
    <Dialog open={open} onOpenChange={setOpen} dismissible={dismissible}>
      <DialogContent closeLabel="Close" dismissible={dismissible}>
        <DialogTitle>Title</DialogTitle>
        <DialogFooter>
          <button type="button" onClick={() => setOpen(false)}>
            Done
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

describe("Dialog dismissible", () => {
  test("Escape closes the dialog when dismissible (the default)", async () => {
    render(<Harness dismissible />);
    expect(screen.getByText("Title")).toBeVisible();

    await userEvent.keyboard("{Escape}");

    expect(screen.queryByText("Title")).not.toBeInTheDocument();
  });

  test("Escape does not close the dialog when dismissible is false", async () => {
    render(<Harness dismissible={false} />);
    expect(screen.getByText("Title")).toBeVisible();

    await userEvent.keyboard("{Escape}");

    expect(screen.getByText("Title")).toBeVisible();
  });

  test("the corner close button is disabled when dismissible is false", () => {
    render(<Harness dismissible={false} />);

    expect(screen.getByRole("button", { name: "Close" })).toBeDisabled();
  });

  test("an explicit close (not routed through the dialog's own dismiss) still works when not dismissible", async () => {
    render(<Harness dismissible={false} />);

    await userEvent.click(screen.getByRole("button", { name: "Done" }));

    expect(screen.queryByText("Title")).not.toBeInTheDocument();
  });
});
