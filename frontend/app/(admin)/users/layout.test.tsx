import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

import type { CurrentIdentity } from "@/lib/auth/session";

import { RowCheckbox, useSelectedIds } from "./selection";

// `layout.tsx` reads the signed-in administrator through `fetchIdentity`,
// which calls `next/headers`'s `cookies()` — unavailable outside a real
// request. The whole module is faked, the same way `selection.test.tsx`
// fakes `./bulk-actions`: what is under test here is what `UsersLayout` does
// with an identity, not how one is fetched.
const { fetchIdentity } = vi.hoisted(() => ({ fetchIdentity: vi.fn() }));
vi.mock("@/lib/auth/session", () => ({ fetchIdentity }));

import UsersLayout from "./layout";

function identity(id: string): CurrentIdentity {
  return { id, login: id, fullName: id, roles: [], permissions: [] };
}

/**
 * Proves the mechanism Change 1 relies on: a search does not create a new
 * `page.tsx` render from scratch in isolation — it re-renders `page.tsx`
 * while the layout above it (this file) keeps its own React identity. Per
 * Next's own docs (`node_modules/next/dist/docs/01-app/03-api-reference/03-file-conventions/layout.md`,
 * "Layouts do not rerender on navigation"), that is exactly what happens
 * when only `searchParams` change on the same route: the layout is not
 * torn down and rebuilt, only its `children` prop is swapped for the next
 * page render.
 *
 * `rerender` reproduces that precisely: the same `<SelectionProvider>`
 * element (the resolved output of awaiting this async layout — testing
 * library can `render`/`rerender` only an already-resolved element, not a
 * function that itself returns a promise), a completely different subtree
 * passed as `children`. React keeps that element's component (and inside it,
 * the `useState` `SelectionProvider` holds) mounted across that, exactly as
 * the framework does across a search — this is a reconciliation test, not a
 * live run of the Next dev server, which the task deliberately avoids
 * pointing at the local database.
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

    // Stands in for what `page.tsx` does on every search: a wholly different
    // set of rows, rendered as this same layout's `children`. Same
    // administrator both times — nothing here changes who is signed in.
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

    // "a" is off screen — this search's results do not include it — but the
    // store the layout is still holding has to remember it was picked.
    expect(screen.queryByRole("checkbox", { name: "a" })).not.toBeInTheDocument();
    expect(screen.getByTestId("ids")).toHaveTextContent("a");
  });

  test("a genuinely new mount starts with nothing picked", async () => {
    fetchIdentity.mockResolvedValue(identity("admin-1"));

    // The contrast case: an actual first visit — a fresh `render`, not a
    // `rerender` of an existing tree — gets a fresh store, exactly as
    // `page.tsx` used to guarantee by owning the provider itself.
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

  // Finding 4: the reviewer could not rule out that a back-navigation, after
  // a sign-out and a different administrator signing in in the same tab,
  // might replay a cached copy of this same tree — the identical React
  // element this test's own `rerender` above stands in for. Rather than
  // depend on knowing whether that replay is possible, the layout is made to
  // not matter either way: `SelectionProvider` clears the store the moment
  // the administrator it is given differs from the one it last held picks
  // for, so it does not matter *why* this instance is being asked about
  // someone else — a fresh mount, a cache replay, anything in between.
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

    // `fetchIdentity` reports no session the same way it does for a signed-out
    // visitor: null, not a thrown error.
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
