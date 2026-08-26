import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import { StateView } from "./state-view";

/**
 * The pairs are what this component exists to keep apart, so they are what the
 * tests hold: an empty screen that offers a filter reset is lying about having
 * a filter, and a dead end that offers a retry is lying about being retryable.
 */
describe("StateView, the empty pair", () => {
  test("offers no way to clear a filter when no filter is what emptied the screen", () => {
    render(<StateView state={{ kind: "empty", title: "No contests yet", body: "Create one." }} />);

    expect(screen.getByRole("heading", { name: "No contests yet" })).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  test("offers the reset when a filter is what emptied it", () => {
    render(
      <StateView
        state={{
          kind: "empty-filtered",
          title: "Nothing matched",
          body: "No contest fits.",
          reset: { label: "Clear filters", href: "/contests" },
        }}
      />,
    );

    expect(screen.getByRole("link", { name: "Clear filters" })).toHaveAttribute(
      "href",
      "/contests",
    );
  });
});

describe("StateView, the error pair", () => {
  test("retries when the failure is worth retrying", async () => {
    const onRetry = vi.fn();
    render(
      <StateView
        state={{
          kind: "error-recoverable",
          title: "Could not load",
          body: "The server did not answer.",
          retry: { label: "Try again", onRetry },
        }}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(onRetry).toHaveBeenCalledOnce();
  });

  test("offers no retry for a failure a second attempt cannot change", () => {
    render(
      <StateView
        state={{
          kind: "error-terminal",
          title: "No access",
          body: "This contest belongs to somebody else.",
          exit: { label: "Back", href: "/contests" },
        }}
      />,
    );

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back" })).toBeInTheDocument();
  });
});

describe("StateView, blocked", () => {
  test("names what is missing rather than greying a control out in silence", () => {
    render(
      <StateView
        state={{
          kind: "blocked",
          title: "Not ready to publish",
          body: "Some translations are missing.",
          detail: "ru: story, 5 questions",
          badge: "draft",
        }}
      />,
    );

    expect(screen.getByText("ru: story, 5 questions")).toBeInTheDocument();
    expect(screen.getByText("draft")).toBeInTheDocument();
  });
});
