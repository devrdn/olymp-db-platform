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

/** Controlled harness shaped like the real caller (`person-picker.tsx`). */
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

  // Passing an id down is not enough; the input must point at it.
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
  // With client-side re-filtering, a query matched on a field the label does
  // not show would hide rows the server returned.
  test("shows every passed item regardless of what the box currently holds — no client-side re-filtering", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    // Neither label contains "zzz"; a re-filtering combobox would show none.
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

  // Compared by `Object.is`, the chosen option would stop being recognised
  // after the next debounced re-fetch.
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
            // Same keys, new objects: the shape of a real re-fetch.
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

    // Reopen without touching the text, to isolate object recognition from text
    // matching.
    await user.click(screen.getByRole("combobox"));

    const option = await screen.findByRole("option", { name: /Ivanov Sergei/ });
    expect(option).toHaveAttribute("data-selected", "");
  });
});

describe("Combobox, status and disabled state", () => {
  test("announces the status message in a live region", () => {
    render(<Harness statusMessage="Searching…" />);

    // Substring match: the primitive appends an invisible character so a
    // repeated status is re-announced.
    const status = screen.getByText((content) => content.includes("Searching…"));
    expect(status).toHaveAttribute("aria-live", "polite");
  });

  test("disables the input when asked", () => {
    render(<Harness disabled />);

    expect(screen.getByRole("combobox")).toBeDisabled();
  });
});

describe("Combobox, with an explanation behind a question mark", () => {
  test("puts the question mark beside the label, leaving the input named by the label alone", () => {
    render(
      <Combobox
        id="picker"
        label="Person"
        items={OPTIONS}
        inputValue=""
        onInputValueChange={() => {}}
        value={null}
        onValueChange={() => {}}
        emptyMessage="No matches"
        help="Search by login, name or email, then choose from the list."
        helpLabel="Hint"
      />,
    );

    expect(screen.getByRole("combobox", { name: "Person" })).toHaveAccessibleName("Person");
    expect(screen.getByRole("button", { name: "Hint" })).toHaveAccessibleDescription(
      "Search by login, name or email, then choose from the list.",
    );
  });
});
