import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted, so the mock is built with `vi.hoisted`.
const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/client")>();
  return { ...actual, request };
});

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { PersonPicker } from "./person-picker";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  request.mockReset();
});

/** Inside a real `<form>`, so the hidden input reaches a submit. */
function renderInForm() {
  const onSubmit = vi.fn();
  render(
    <form
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(new FormData(event.currentTarget));
      }}
    >
      <PersonPicker
        id="participantId"
        name="userId"
        contestId="11111111-1111-1111-1111-111111111111"
        label={en.workspace.people.addOne.heading}
        placeholder={en.workspace.people.picker.placeholder}
        helpText={en.workspace.people.picker.helpText}
        searchingText={en.workspace.people.picker.searching}
        noResultsText={en.workspace.people.picker.noResults}
        searchFailedText={en.workspace.people.picker.searchFailed}
        changeText={en.workspace.people.picker.change}
        selectedTemplate={en.workspace.people.picker.selected}
      />
      <button type="submit">Submit</button>
    </form>,
  );
  return onSubmit;
}

const candidate = {
  user_id: "22222222-2222-2222-2222-222222222222",
  login: "s.ivanov",
  full_name: "Ivanov Sergei",
};

describe("PersonPicker, debouncing", () => {
  // Real timers: Base UI's combobox stalls under fake ones, and waiting 300ms
  // costs little.
  test("a burst of keystrokes is one request, not one per letter", async () => {
    request.mockResolvedValue({ items: [candidate] });
    const user = userEvent.setup();
    renderInForm();

    await user.type(screen.getByRole("combobox"), "ivan");

    // The pause has not elapsed yet.
    expect(request).not.toHaveBeenCalled();

    await new Promise((resolve) => setTimeout(resolve, 350));

    expect(request).toHaveBeenCalledTimes(1);
    expect(request).toHaveBeenCalledWith(expect.stringContaining("q=ivan"));
  });

  test("an empty or whitespace-only box asks nothing at all", async () => {
    const user = userEvent.setup();
    renderInForm();

    await user.type(screen.getByRole("combobox"), "   ");
    // Past the debounce window, so this is not a false negative.
    await new Promise((resolve) => setTimeout(resolve, 350));

    expect(request).not.toHaveBeenCalled();
  });

  test("a query shorter than the server's minimum asks nothing at all", async () => {
    // Below the server's minimum the answer is always empty.
    const user = userEvent.setup();
    renderInForm();

    await user.type(screen.getByRole("combobox"), "iv");
    await new Promise((resolve) => setTimeout(resolve, 350));

    expect(request).not.toHaveBeenCalled();
  });
});

describe("PersonPicker, choosing a result", () => {
  test("submits the candidate's id, not their name or login", async () => {
    request.mockResolvedValue({ items: [candidate] });
    const user = userEvent.setup();
    const onSubmit = renderInForm();

    await user.type(screen.getByRole("combobox"), "ivanov");
    const option = await screen.findByText("Ivanov Sergei");
    await user.click(option);

    await user.click(screen.getByRole("button", { name: "Submit" }));

    expect(onSubmit).toHaveBeenCalledTimes(1);
    const data = onSubmit.mock.calls[0][0] as FormData;
    expect(data.get("userId")).toBe(candidate.user_id);
  });

  test("Enter chooses the highlighted match from the keyboard", async () => {
    request.mockResolvedValue({ items: [candidate] });
    const user = userEvent.setup();
    const onSubmit = renderInForm();

    await user.type(screen.getByRole("combobox"), "ivanov");
    await screen.findByText("Ivanov Sergei");

    await user.keyboard("{ArrowDown}{Enter}");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    const data = onSubmit.mock.calls[0][0] as FormData;
    expect(data.get("userId")).toBe(candidate.user_id);
  });

  test("the arrow keys move which match Enter would choose", async () => {
    // Two candidates, so auto-highlighting the first cannot pass for arrow
    // movement.
    const second = { user_id: "33333333-3333-3333-3333-333333333333", login: "s.ivanova", full_name: "Ivanova Ana" };
    request.mockResolvedValue({ items: [candidate, second] });
    const user = userEvent.setup();
    const onSubmit = renderInForm();

    await user.type(screen.getByRole("combobox"), "ivan");
    await screen.findByText("Ivanova Ana");

    await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");
    await user.click(screen.getByRole("button", { name: "Submit" }));

    const data = onSubmit.mock.calls[0][0] as FormData;
    expect(data.get("userId")).toBe(second.user_id);
  });

  test("announces the selection with the login that disambiguates it", async () => {
    request.mockResolvedValue({ items: [candidate] });
    const user = userEvent.setup();
    renderInForm();

    await user.type(screen.getByRole("combobox"), "ivanov");
    await user.click(await screen.findByText("Ivanov Sergei"));

    expect(screen.getByText("Selected: Ivanov Sergei (s.ivanov)")).toBeInTheDocument();
  });

  test("Escape dismisses the list without choosing anything", async () => {
    request.mockResolvedValue({ items: [candidate] });
    const user = userEvent.setup();
    const onSubmit = renderInForm();

    await user.type(screen.getByRole("combobox"), "ivanov");
    await screen.findByText("Ivanov Sergei");

    await user.keyboard("{Escape}");

    expect(screen.queryByText("Ivanov Sergei")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Submit" }));
    const data = onSubmit.mock.calls[0][0] as FormData;
    expect(data.get("userId")).toBe("");
  });
});

describe("PersonPicker, showing the email", () => {
  // The email tells same-named accounts apart; without one there must be no
  // stray separator.
  test("an account with an email shows it next to the login", async () => {
    const withEmail = { ...candidate, email: "sergei@example.edu" };
    request.mockResolvedValue({ items: [withEmail] });
    const user = userEvent.setup();
    renderInForm();

    await user.type(screen.getByRole("combobox"), "ivanov");

    expect(await screen.findByText("s.ivanov · sergei@example.edu")).toBeInTheDocument();
  });

  test("an account with no email shows only the login, with no trailing separator", async () => {
    // No `email` key, as `omitempty` sends for an account without one.
    request.mockResolvedValue({ items: [candidate] });
    const user = userEvent.setup();
    renderInForm();

    await user.type(screen.getByRole("combobox"), "ivanov");

    const description = await screen.findByText("s.ivanov");
    expect(description.textContent).toBe("s.ivanov");
  });
});

describe("PersonPicker, what it announces", () => {
  test("the field's accessible name is the label the caller gave it", () => {
    renderInForm();

    expect(screen.getByRole("combobox", { name: en.workspace.people.addOne.heading })).toBeInTheDocument();
  });

  // Pins the `aria-describedby` wiring, not just the id.
  test("the input is described by the paragraph beneath it", () => {
    renderInForm();

    const input = screen.getByRole("combobox");
    expect(input).toHaveAccessibleDescription(en.workspace.people.picker.helpText);
  });
});

describe("PersonPicker, when a search fails", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  // An ApiError is already shown in words; anything else must be logged rather
  // than become an unhandled rejection.
  test("an error that is not an ApiError is logged instead of becoming an unhandled rejection", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    request.mockRejectedValue(new TypeError("network is down"));
    const user = userEvent.setup();
    renderInForm();

    await user.type(screen.getByRole("combobox"), "ivan");
    await new Promise((resolve) => setTimeout(resolve, 350));

    expect(consoleError).toHaveBeenCalled();
  });
});
