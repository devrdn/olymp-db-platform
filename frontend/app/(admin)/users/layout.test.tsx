import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

import type { CurrentIdentity } from "@/lib/auth/session";

import { RowCheckbox, useSelectedIds } from "./selection";

// `fetchIdentity` calls `cookies()`, unavailable outside a request; the test
// covers what the layout does with an identity.
const { fetchIdentity } = vi.hoisted(() => ({ fetchIdentity: vi.fn() }));
vi.mock("@/lib/auth/session", () => ({ fetchIdentity }));

import UsersLayout from "./layout";

function identity(id: string): CurrentIdentity {
  return { id, login: id, fullName: id, roles: [], permissions: [] };
}

/**
 * A search re-renders `page.tsx` while the layout keeps its React identity
 * (Next: "Layouts do not rerender on navigation"). `rerender` with the same
 * resolved `<SelectionProvider>` element and new children reproduces that, so
 * the provider's state stays mounted.
 */
function Reader() {
  const ids = useSelectedIds();
  return <p data-testid="ids">{ids.join(",")}</p>;
}

describe("UsersLayout", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test("a pick survives what a search does to the page beneath it", async () => {
    fetchIdentity.mockResolvedValue(identity("admin-1"));

    const { rerender } = render(
      await UsersLayout({
        children: (
          <>
            <RowCheckbox id="a" label="a" />
            <Reader />
          </>
        ),
      }),
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));
    expect(screen.getByTestId("ids")).toHaveTextContent("a");

    // A search: different rows as the same layout's children, same
    // administrator.
    rerender(
      await UsersLayout({
        children: (
          <>
            <RowCheckbox id="b" label="b" />
            <Reader />
          </>
        ),
      }),
    );

    // "a" is off screen, but the store must remember it.
    expect(screen.queryByRole("checkbox", { name: "a" })).not.toBeInTheDocument();
    expect(screen.getByTestId("ids")).toHaveTextContent("a");
  });

  test("a genuinely new mount starts with nothing picked", async () => {
    fetchIdentity.mockResolvedValue(identity("admin-1"));

    // A fresh `render` is a first visit and gets a fresh store.
    render(
      await UsersLayout({
        children: (
          <>
            <RowCheckbox id="a" label="a" />
            <Reader />
          </>
        ),
      }),
    );

    expect(screen.getByTestId("ids")).toHaveTextContent("");
  });

  // A back navigation after a different administrator signs in might replay a
  // cached tree; the provider clears the store whenever the administrator
  // changes, whatever the cause.
  test("a pick does not survive a change of who is signed in", async () => {
    fetchIdentity.mockResolvedValue(identity("admin-1"));

    const { rerender } = render(
      await UsersLayout({
        children: (
          <>
            <RowCheckbox id="a" label="a" />
            <Reader />
          </>
        ),
      }),
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));
    expect(screen.getByTestId("ids")).toHaveTextContent("a");

    fetchIdentity.mockResolvedValue(identity("admin-2"));

    rerender(
      await UsersLayout({
        children: (
          <>
            <RowCheckbox id="a" label="a" />
            <Reader />
          </>
        ),
      }),
    );

    expect(screen.getByTestId("ids")).toHaveTextContent("");
  });

  test("a pick does not survive signing out (no identity to hand it to)", async () => {
    fetchIdentity.mockResolvedValue(identity("admin-1"));

    const { rerender } = render(
      await UsersLayout({
        children: (
          <>
            <RowCheckbox id="a" label="a" />
            <Reader />
          </>
        ),
      }),
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));
    expect(screen.getByTestId("ids")).toHaveTextContent("a");

    // No session is `null`, not a thrown error.
    fetchIdentity.mockResolvedValue(null);

    rerender(
      await UsersLayout({
        children: (
          <>
            <RowCheckbox id="a" label="a" />
            <Reader />
          </>
        ),
      }),
    );

    expect(screen.getByTestId("ids")).toHaveTextContent("");
  });
});
