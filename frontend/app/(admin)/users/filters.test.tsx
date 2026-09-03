import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

const replace = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ replace }) }));

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { AccountFilters } from "./filters";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

/**
 * The box is uncontrolled on purpose: a value re-rendered from the server on
 * every navigation is how the caret jumps to the end of a half-typed word.
 *
 * The cost of that choice is this: React does not update an uncontrolled
 * input when `defaultValue` changes, so anything that moves the query from
 * outside — the back button, the reset link — changes the list while the box
 * keeps the old text. The screen then shows results for one search under a box
 * claiming another.
 */
describe("AccountFilters, when the query changes from outside", () => {
  test("follows the address when the visitor goes back", () => {
    const { rerender } = render(<AccountFilters query="popescu" status="" dict={en} />);
    const box = screen.getByLabelText(en.accounts.search) as HTMLInputElement;
    expect(box.value).toBe("popescu");

    // What Back does: the URL is the state, so the prop is what moved.
    rerender(<AccountFilters query="ivanov" status="" dict={en} />);

    expect(box.value).toBe("ivanov");
  });

  test("empties itself when the filters are reset", () => {
    const { rerender } = render(<AccountFilters query="popescu" status="" dict={en} />);
    const box = screen.getByLabelText(en.accounts.search) as HTMLInputElement;

    rerender(<AccountFilters query="" status="" dict={en} />);

    expect(box.value).toBe("");
  });

  test("leaves the box alone while somebody is typing in it", () => {
    // The other half, and the reason focus is the discriminator. Typing
    // navigates, so the address catches up a moment later — by which time the
    // typist is two letters further on. Writing the address back into a
    // focused box would swallow those two letters, which is exactly the caret
    // jump the uncontrolled input was chosen to avoid.
    const { rerender } = render(<AccountFilters query="" status="" dict={en} />);
    const box = screen.getByLabelText(en.accounts.search) as HTMLInputElement;

    box.focus();
    box.value = "popescu ty";
    // The navigation for "popescu" lands while "ty" has already been typed.
    rerender(<AccountFilters query="popescu" status="" dict={en} />);

    expect(box.value).toBe("popescu ty");
  });

  test("follows the status filter, which nobody types into", () => {
    const { rerender } = render(<AccountFilters query="" status="active" dict={en} />);
    const picker = screen.getByLabelText(en.accounts.filter) as HTMLSelectElement;
    expect(picker.value).toBe("active");

    rerender(<AccountFilters query="" status="blocked" dict={en} />);

    expect(picker.value).toBe("blocked");
  });
});

/**
 * The status filter is driven off `ACCOUNT_STATUSES`, which already carries
 * "deleted" (task 8). What this task adds is honesty about what the empty
 * option means: the backend reads it as "everyone except the deleted", not
 * as "everyone" — see `users.Filter` and the comment on the option in
 * `filters.tsx`.
 */
describe("AccountFilters, the status options", () => {
  test("offers deleted as a status to filter by, without a second definition", () => {
    render(<AccountFilters query="" status="" dict={en} />);
    const picker = screen.getByLabelText(en.accounts.filter) as HTMLSelectElement;

    const values = [...picker.options].map((option) => option.value);
    expect(values).toContain("deleted");
  });

  test("labels the empty option as excluding deleted accounts, not as 'all' of them", () => {
    render(<AccountFilters query="" status="" dict={en} />);
    const picker = screen.getByLabelText(en.accounts.filter) as HTMLSelectElement;
    const label = picker.options[0]?.textContent ?? "";

    // A bare "All" or "Any state" would tell an administrator this shows
    // every account when it deliberately does not; the label has to name
    // what is left out.
    expect(label.toLowerCase()).not.toBe("all");
    expect(label.toLowerCase()).not.toBe("any state");
    expect(label.toLowerCase()).toContain("deleted");
  });
});
