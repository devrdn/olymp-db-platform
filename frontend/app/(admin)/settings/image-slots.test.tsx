import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Actions would pull in Next's server runtime.
vi.mock("./actions", () => ({
  uploadImageAction: vi.fn(async () => ({})),
  removeImageAction: vi.fn(async () => ({})),
}));

import { ImageSlots } from "./image-slots";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

/** The accepted-format rule stays on screen; only the reasons move behind the "?". */
describe("ImageSlots, the accepted-format rule and its reasons", () => {
  test("shows what is accepted before anything is chosen", () => {
    render(<ImageSlots images={{}} dict={dict} />);

    expect(screen.getByText(dict.settings.images.hint)).toBeVisible();
  });

  test("keeps the reasons closed until the question mark beside the heading is pressed", async () => {
    const user = userEvent.setup();
    render(<ImageSlots images={{}} dict={dict} />);

    const reasons = screen.getByText(dict.settings.images.help);
    expect(reasons).not.toBeVisible();

    const hint = screen
      .getAllByRole("button", { name: dict.chrome.helpLabel })
      .find((button) => button.getAttribute("aria-describedby") === reasons.id)!;
    await user.click(hint);

    expect(reasons).toBeVisible();
  });

  test("puts what each mark is for behind its own question mark", () => {
    render(<ImageSlots images={{}} dict={dict} />);

    const t = dict.settings.images;
    // One for the section, one per slot.
    expect(screen.getAllByRole("button", { name: dict.chrome.helpLabel })).toHaveLength(4);
    for (const help of [t.logoHelp, t.iconHelp, t.faviconHelp]) {
      expect(screen.getByText(help)).not.toBeVisible();
    }
  });
});
