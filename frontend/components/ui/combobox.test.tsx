import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import { Combobox, type ComboboxOption } from "./combobox";

type Item = { id: string };

const OPTIONS: ComboboxOption<Item>[] = [
  { key: "1", value: { id: "1" }, label: "Ivanov Sergei", description: "s.ivanov" },
  { key: "2", value: { id: "2" }, label: "Petrova Anna", description: "a.petrova" },
];

/**
 * A small controlled harness, the same shape every real caller uses
 * (`person-picker.tsx` is the only one today): `inputValue` and `value` live
 * in the caller's own state, exactly as the docstring on `Combobox` says a
 * picker whose items are re-fetched every keystroke has to hold them.
 */
function Harness({
  items = OPTIONS,
  statusMessage,
  disabled,
  describedBy,
}: {
  items?: ComboboxOption<Item>[];
  statusMessage?: string;
  disabled?: boolean;
  describedBy?: string;
}) {
  const [inputValue, setInputValue] = useState("");
  const [value, setValue] = useState<ComboboxOption<Item> | null>(null);
  return (
    <Combobox
      id="picker"
      label="Person"
      items={items}
      inputValue={inputValue}
      onInputValueChange={setInputValue}
      value={value}
      onValueChange={setValue}
      emptyMessage="No matches"
      statusMessage={statusMessage}
      disabled={disabled}
      describedBy={describedBy}
    />
  );
}

describe("Combobox, label and description", () => {
  test("the input's accessible name is the label", () => {
    render(<Harness />);

    expect(screen.getByRole("combobox", { name: "Person" })).toBeInTheDocument();
  });

  // Finding 3: a describing paragraph elsewhere on the page is only
  // announced to a screen reader if something actually points the input at
  // it — passing an id down is not enough on its own.
  test("wires describedBy to the input's aria-describedby", () => {
    render(
      <>
        <Harness describedBy="picker-help" />
        <p id="picker-help">Type to search.</p>
      </>,
    );

    expect(screen.getByRole("combobox")).toHaveAccessibleDescription("Type to search.");
  });

  test("carries no description when the caller gives none", () => {
    render(<Harness />);

    expect(screen.getByRole("combobox")).not.toHaveAttribute("aria-describedby");
  });
});

describe("Combobox, the item list", () => {
  // The component's own docstring: `filter={null}` turns off the primitive's
  // client-side re-filtering, because every caller here already fetched an
  // already-matched result set from the server. If that were left on, typing
  // something that does not literally appear in an option's *label* would
  // hide options the server was right to return (a login or an email
  // matched, folded into a full-name label the box does not show).
  test("shows every passed item regardless of what the box currently holds — no client-side re-filtering", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    // Neither candidate's label contains "zzz"; a re-filtering combobox
    // would show none of them.
    await user.type(screen.getByRole("combobox"), "zzz");

    expect(await screen.findByText("Ivanov Sergei")).toBeInTheDocument();
    expect(screen.getByText("Petrova Anna")).toBeInTheDocument();
  });

  test("renders each option's description as a second line", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(screen.getByRole("combobox"), "i");

    const option = await screen.findByRole("option", { name: /Ivanov Sergei/ });
    expect(option).toHaveTextContent("s.ivanov");
  });

  test("shows the empty message once there really is nothing to offer", async () => {
    const user = userEvent.setup();
    render(<Harness items={[]} />);

    await user.type(screen.getByRole("combobox"), "anything");

    expect(await screen.findByText("No matches")).toBeInTheDocument();
  });
});

describe("Combobox, choosing an option", () => {
  test("clicking an option reports its value and fills the input with its label", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(screen.getByRole("combobox"), "ivanov");
    await user.click(await screen.findByText("Ivanov Sergei"));

    expect(screen.getByRole("combobox")).toHaveValue("Ivanov Sergei");
  });

  // The component's own docstring: selection is compared by `key`, not by
  // object identity, because a picker whose items come back from a fresh
  // request every keystroke never has the same object twice — even for the
  // option already chosen. A version of this component keyed off
  // `Object.is` (the primitive's own default, before `isItemEqualToValue`
  // was passed) would stop recognising the chosen option the moment `items`
  // is replaced by a new array, which is exactly what the next debounced
  // search does.
  test("keeps recognising the chosen option once items is replaced by a same-key, different-object array", async () => {
    function KeyIdentityHarness() {
      const [inputValue, setInputValue] = useState("");
      const [value, setValue] = useState<ComboboxOption<Item> | null>(null);
      const [items, setItems] = useState(OPTIONS);
      return (
        <Combobox
          id="picker"
          label="Person"
          items={items}
          inputValue={inputValue}
          onInputValueChange={setInputValue}
          value={value}
          onValueChange={(next) => {
            setValue(next);
            // A fresh array with the same keys but new option objects — the
            // shape of a real re-fetch, not the same reference chosen twice.
            setItems(OPTIONS.map((o) => ({ ...o })));
          }}
          emptyMessage="No matches"
        />
      );
    }

    const user = userEvent.setup();
    render(<KeyIdentityHarness />);

    await user.type(screen.getByRole("combobox"), "ivanov");
    await user.click(await screen.findByText("Ivanov Sergei"));
    expect(screen.getByRole("combobox")).toHaveValue("Ivanov Sergei");

    // Reopen the popup without touching the text, so this isolates whether
    // the chosen *object* is still recognised now that `items` holds all
    // new objects — not whether the displayed text still matches, which
    // would be a different question.
    await user.click(screen.getByRole("combobox"));

    const option = await screen.findByRole("option", { name: /Ivanov Sergei/ });
    expect(option).toHaveAttribute("data-selected", "");
  });
});

describe("Combobox, status and disabled state", () => {
  test("announces the status message in a live region", () => {
    render(<Harness statusMessage="Searching…" />);

    // A substring match, not an exact one: the primitive appends an
    // invisible character to its own status text so a screen reader
    // re-announces it even when the same words repeat back to back.
    const status = screen.getByText((content) => content.includes("Searching…"));
    expect(status).toHaveAttribute("aria-live", "polite");
  });

  test("disables the input when asked", () => {
    render(<Harness disabled />);

    expect(screen.getByRole("combobox")).toBeDisabled();
  });
});
