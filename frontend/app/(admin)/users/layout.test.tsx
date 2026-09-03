import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import { RowCheckbox, useSelectedIds } from "./selection";

import UsersLayout from "./layout";

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
 * `rerender` reproduces that precisely: the same `<UsersLayout>` element,
 * a completely different subtree passed as `children`. React keeps the
 * `UsersLayout` (and, inside it, the `SelectionProvider`'s own `useState`)
 * mounted across that, exactly as the framework does across a search — this
 * is a reconciliation test, not a live run of the Next dev server, which the
 * task deliberately avoids pointing at the local database.
 */
function Reader() {
  const ids = useSelectedIds();
  return <p data-testid="ids">{ids.join(",")}</p>;
}

describe("UsersLayout", () => {
  test("a pick survives what a search does to the page beneath it", async () => {
    const { rerender } = render(
      <UsersLayout>
        <RowCheckbox id="a" label="a" />
        <Reader />
      </UsersLayout>,
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));
    expect(screen.getByTestId("ids")).toHaveTextContent("a");

    // Stands in for what `page.tsx` does on every search: a wholly different
    // set of rows, rendered as this same layout's `children`.
    rerender(
      <UsersLayout>
        <RowCheckbox id="b" label="b" />
        <Reader />
      </UsersLayout>,
    );

    // "a" is off screen — this search's results do not include it — but the
    // store the layout is still holding has to remember it was picked.
    expect(screen.queryByRole("checkbox", { name: "a" })).not.toBeInTheDocument();
    expect(screen.getByTestId("ids")).toHaveTextContent("a");
  });

  test("a genuinely new mount starts with nothing picked", () => {
    // The contrast case: an actual first visit — a new `<UsersLayout>`
    // element, not a `rerender` of an existing one — gets a fresh store,
    // exactly as `page.tsx` used to guarantee by owning the provider itself.
    render(
      <UsersLayout>
        <RowCheckbox id="a" label="a" />
        <Reader />
      </UsersLayout>,
    );

    expect(screen.getByTestId("ids")).toHaveTextContent("");
  });
});
