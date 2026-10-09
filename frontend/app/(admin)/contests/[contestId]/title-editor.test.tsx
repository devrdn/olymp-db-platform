import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { Contest } from "@/lib/api/contests";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in Next's server runtime; `vi.hoisted`
// because `vi.mock` factories run first.
const { saveTranslationsAction } = vi.hoisted(() => ({
  // Typed so `.mock.calls[0][1]` is a `FormData`.
  saveTranslationsAction: vi.fn(async (previous: unknown, form: FormData) => {
    void previous;
    void form;
    return { saved: true };
  }),
}));
vi.mock("./actions", () => ({ saveTranslationsAction }));

import { TitleEditor } from "./title-editor";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  saveTranslationsAction.mockClear();
});

function contest(overrides: Partial<Contest> = {}): Contest {
  return {
    id: "f767af3b-f135-40d2-a3a6-82d368de1004",
    status: "draft",
    enrollment: "invite_only",
    questionMode: "multi",
    timing: "fixed",
    durationMin: undefined,
    startsAt: "2026-11-08T17:00:00Z",
    endsAt: "2026-11-08T21:30:00Z",
    allowedCidrs: [],
    settings: { queryRateLimitPerMin: 0, gracePeriodMin: 0 },
    languages: [
      { code: "en", isDefault: true },
      { code: "ru", isDefault: false },
    ],
    translations: {
      en: { title: "Night in the archive" },
      ru: { title: "Ночь в архиве" },
    },
    createdAt: "2026-08-01T10:00:00Z",
    updatedAt: "2026-08-31T17:00:00Z",
    ...overrides,
  } as Contest;
}

describe("TitleEditor: reachability", () => {
  test("offers no control at all when the contest is not editable", () => {
    render(<TitleEditor contest={contest()} editable={false} dict={dict} />);

    expect(screen.queryByRole("button", { name: dict.workspace.titleEditor.edit })).not.toBeInTheDocument();
  });

  test("a single click reaches the title field — nothing to hunt through", async () => {
    render(<TitleEditor contest={contest()} editable dict={dict} />);

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.titleEditor.edit }));

    // Found by value, since every field here shares the "Title" label.
    expect(screen.getByDisplayValue("Night in the archive")).toBeVisible();
  });
});

describe("TitleEditor: the default language is the short path, the rest stay reachable", () => {
  test("the disclosure holding every other declared language starts closed", async () => {
    render(<TitleEditor contest={contest()} editable dict={dict} />);
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.titleEditor.edit }));

    // jsdom does not hide a closed `<details>`, so check its `open` state.
    const disclosure = screen.getByText(dict.workspace.titleEditor.otherLanguages).closest("details");
    expect(disclosure).not.toBeNull();
    expect(disclosure).not.toHaveAttribute("open");
  });

  test("a second declared language is one click away, and stays in the form either way", async () => {
    render(<TitleEditor contest={contest()} editable dict={dict} />);
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.titleEditor.edit }));

    // Present, and so submitted, even while collapsed.
    expect(screen.getByDisplayValue("Ночь в архиве")).toBeInTheDocument();

    await userEvent.click(screen.getByText(dict.workspace.titleEditor.otherLanguages));

    const disclosure = screen.getByText(dict.workspace.titleEditor.otherLanguages).closest("details");
    expect(disclosure).toHaveAttribute("open");
  });
});

describe("TitleEditor: editing reaches the server action", () => {
  test("changing the title and saving sends both declared languages, not just the one edited", async () => {
    render(<TitleEditor contest={contest()} editable dict={dict} />);
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.titleEditor.edit }));

    const field = screen.getByDisplayValue("Night in the archive");
    await userEvent.clear(field);
    await userEvent.type(field, "Night in the archive, revised");

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.settings.save }));

    await waitFor(() => expect(saveTranslationsAction).toHaveBeenCalled());
    const form = saveTranslationsAction.mock.calls[0]![1] as FormData;
    expect(form.get("contestId")).toBe("f767af3b-f135-40d2-a3a6-82d368de1004");
    expect(form.get("title.en")).toBe("Night in the archive, revised");
    // The whole set is replaced, so omitting it would delete it.
    expect(form.get("title.ru")).toBe("Ночь в архиве");
  });

  test("closes itself once the save reports success — the header behind it already shows the new title", async () => {
    render(<TitleEditor contest={contest()} editable dict={dict} />);
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.titleEditor.edit }));

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.settings.save }));

    await waitFor(() =>
      expect(screen.queryByLabelText(dict.workspace.titleEditor.title)).not.toBeInTheDocument(),
    );
  });
});

describe("TitleEditor: dismissal is blocked while the save is pending", () => {
  test("Escape does not close the dialog mid-request", async () => {
    let resolveAction: (value: { saved: boolean }) => void = () => {};
    saveTranslationsAction.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveAction = resolve;
        }),
    );
    render(<TitleEditor contest={contest()} editable dict={dict} />);
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.titleEditor.edit }));

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.settings.save }));
    await screen.findByRole("button", { name: dict.workspace.settings.saving });

    await userEvent.keyboard("{Escape}");

    expect(screen.getByRole("button", { name: dict.workspace.settings.saving })).toBeVisible();

    resolveAction({ saved: true });
    await waitFor(() =>
      expect(screen.queryByLabelText(dict.workspace.titleEditor.title)).not.toBeInTheDocument(),
    );
  });
});

/** The "?" inside a modal: one Escape closes the bubble, the next the dialog. */
describe("TitleEditor: the explanation beside the title", () => {
  test("Escape closes the explanation and leaves the dialog open", async () => {
    const user = userEvent.setup();
    render(<TitleEditor contest={contest()} editable dict={dict} />);

    await user.click(screen.getByRole("button", { name: dict.workspace.titleEditor.edit }));
    const dialog = await screen.findByRole("dialog");
    // Named by the title alone.
    expect(dialog).toHaveAccessibleName(dict.workspace.titleEditor.heading);

    await user.click(screen.getByRole("button", { name: dict.chrome.helpLabel }));
    expect(screen.getByRole("tooltip")).toHaveTextContent(dict.workspace.titleEditor.help);

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
