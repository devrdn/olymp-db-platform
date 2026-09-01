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
