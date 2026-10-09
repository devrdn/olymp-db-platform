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
 * The box is uncontrolled so the caret does not jump, so a query changed from
 * outside (Back, reset) must be written into it explicitly.
 */
describe("AccountFilters, when the query changes from outside", () => {
  test("follows the address when the visitor goes back", () => {
    const { rerender } = render(<AccountFilters query="popescu" status="" dict={en} />);
    const box = screen.getByLabelText(en.accounts.search) as HTMLInputElement;
    expect(box.value).toBe("popescu");

    // What Back does: the prop moves.
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
    // Typing navigates and the address lags; writing it back into a focused box
    // would swallow the letters typed since.
    const { rerender } = render(<AccountFilters query="" status="" dict={en} />);
    const box = screen.getByLabelText(en.accounts.search) as HTMLInputElement;

    box.focus();
    box.value = "popescu ty";
    // The navigation for "popescu" lands after "ty" was typed.
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
 * The empty status option means everyone except the deleted (`users.Filter`),
 * and its label must say so.
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

    // The label must name what is left out.
    expect(label.toLowerCase()).not.toBe("all");
    expect(label.toLowerCase()).not.toBe("any state");
    expect(label.toLowerCase()).toContain("deleted");
  });
});
