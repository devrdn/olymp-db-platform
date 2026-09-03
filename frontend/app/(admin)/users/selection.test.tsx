import { Profiler } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { RowCheckbox, SelectAllCheckbox, SelectionBar, SelectionProvider, useSelectedIds } from "./selection";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

/**
 * Wraps a row in a `Profiler` and counts how many times React actually
 * commits that subtree.
 *
 * A plain wrapper component that merely renders `<RowCheckbox/>` as a child
 * would never register a re-render here no matter what RowCheckbox
 * subscribes to — a child updating never forces its parent to re-render, so
 * counting the parent's own render calls proves nothing. `Profiler.onRender`
 * fires once per commit of the subtree it wraps, including a commit that
 * originates inside a descendant, which is the count that actually tells
 * apart "only this row updated" from "every row updated and bailed out
 * looking unchanged".
 */
function Counted({ id, onRender }: { id: string; onRender: () => void }) {
  return (
    <Profiler id={id} onRender={onRender}>
      <RowCheckbox id={id} label={id} />
    </Profiler>
  );
}

describe("selection store", () => {
  test("selecting a row re-renders that row and not its neighbours", async () => {
    const renders = { first: 0, second: 0 };
    // Counting commits is the assertion: a context that re-renders every row
    // on every click is exactly what this store exists to avoid, and only a
    // count catches it.
    render(
      <SelectionProvider>
        <Counted onRender={() => renders.first++} id="a" />
        <Counted onRender={() => renders.second++} id="b" />
      </SelectionProvider>,
    );
    const before = renders.second;

    await userEvent.click(screen.getByRole("checkbox", { name: /a/ }));

    expect(renders.second).toBe(before);
  });

  test("ticking a row's box marks it checked, and ticking again clears it", async () => {
    render(
      <SelectionProvider>
        <RowCheckbox id="a" label="a" />
      </SelectionProvider>,
    );
    const box = screen.getByRole("checkbox", { name: "a" });
    expect(box).not.toBeChecked();

    await userEvent.click(box);
    expect(box).toBeChecked();

    await userEvent.click(box);
    expect(box).not.toBeChecked();
  });

  test("two rows are independent: picking one leaves the other alone", async () => {
    render(
      <SelectionProvider>
        <RowCheckbox id="a" label="a" />
        <RowCheckbox id="b" label="b" />
      </SelectionProvider>,
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));

    expect(screen.getByRole("checkbox", { name: "a" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "b" })).not.toBeChecked();
  });
});

describe("SelectAllCheckbox", () => {
  test("picks every id on the page, and clears them on a second click", async () => {
    render(
      <SelectionProvider>
        <SelectAllCheckbox ids={["a", "b"]} label="page" />
        <RowCheckbox id="a" label="a" />
        <RowCheckbox id="b" label="b" />
      </SelectionProvider>,
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "page" }));
    expect(screen.getByRole("checkbox", { name: "a" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "b" })).toBeChecked();

    await userEvent.click(screen.getByRole("checkbox", { name: "page" }));
    expect(screen.getByRole("checkbox", { name: "a" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "b" })).not.toBeChecked();
  });

  test("shows the mixed state while only some rows are picked", async () => {
    render(
      <SelectionProvider>
        <SelectAllCheckbox ids={["a", "b"]} label="page" />
        <RowCheckbox id="a" label="a" />
        <RowCheckbox id="b" label="b" />
      </SelectionProvider>,
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));

    expect(screen.getByRole("checkbox", { name: "page" })).toHaveAttribute("aria-checked", "mixed");
  });

  test("a click while mixed picks the rest, rather than clearing what is already picked", async () => {
    render(
      <SelectionProvider>
        <SelectAllCheckbox ids={["a", "b"]} label="page" />
        <RowCheckbox id="a" label="a" />
        <RowCheckbox id="b" label="b" />
      </SelectionProvider>,
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "page" }));

    expect(screen.getByRole("checkbox", { name: "a" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "b" })).toBeChecked();
  });
});

describe("SelectionBar", () => {
  test("stays out of the page while nothing is picked", () => {
    render(
      <SelectionProvider>
        <SelectionBar dict={en} />
      </SelectionProvider>,
    );

    expect(screen.queryByText(/selected/)).not.toBeInTheDocument();
  });

  test("says how many accounts are picked, and clears them on request", async () => {
    render(
      <SelectionProvider>
        <RowCheckbox id="a" label="a" />
        <RowCheckbox id="b" label="b" />
        <SelectionBar dict={en} />
      </SelectionProvider>,
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "b" }));

    expect(screen.getByText("2 selected")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: en.accounts.selection.clear }));

    expect(screen.queryByText(/selected/)).not.toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "a" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "b" })).not.toBeChecked();
  });
});

describe("useSelectedIds", () => {
  function Reader() {
    const ids = useSelectedIds();
    return <p data-testid="ids">{ids.join(",")}</p>;
  }

  test("reports the ids currently picked", async () => {
    render(
      <SelectionProvider>
        <RowCheckbox id="a" label="a" />
        <RowCheckbox id="b" label="b" />
        <Reader />
      </SelectionProvider>,
    );

    expect(screen.getByTestId("ids")).toHaveTextContent("");

    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));

    expect(screen.getByTestId("ids")).toHaveTextContent("a");
  });
});
