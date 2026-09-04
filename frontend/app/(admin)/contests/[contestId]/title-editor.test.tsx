import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { Contest } from "@/lib/api/contests";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The save action is a Server Action ("use server"): importing the real
// module here would pull Next's server runtime into a component test, the
// same reason `selection.test.tsx` fakes `bulk-actions`. Built with
// `vi.hoisted` because `vi.mock` factories run before the ordinary top-level
// code in this file does.
const { saveTranslationsAction } = vi.hoisted(() => ({
  // Typed as `(previous, form) => Promise<...>` — the action's own shape —
  // purely so `.mock.calls[0][1]` below is a `FormData` and not `unknown`.
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

    // The default language's current title is unique in this fixture, so
    // finding it by value sidesteps every field in this dialog sharing the
    // same "Title" label — English's and Russian's both carry one.
    expect(screen.getByDisplayValue("Night in the archive")).toBeVisible();
  });
});

describe("TitleEditor: the default language is the short path, the rest stay reachable", () => {
  test("the disclosure holding every other declared language starts closed", async () => {
    render(<TitleEditor contest={contest()} editable dict={dict} />);
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.titleEditor.edit }));

    // jsdom does not implement a closed `<details>` hiding its content the
    // way a real browser does, so the field inside it is still findable —
    // proving the collapse means reading the element's own `open` state
    // rather than the visibility of what it holds.
    const disclosure = screen.getByText(dict.workspace.titleEditor.otherLanguages).closest("details");
    expect(disclosure).not.toBeNull();
    expect(disclosure).not.toHaveAttribute("open");
  });

  test("a second declared language is one click away, and stays in the form either way", async () => {
    render(<TitleEditor contest={contest()} editable dict={dict} />);
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.titleEditor.edit }));

    // Present in the DOM, and thus in what a submit sends, even before the
    // disclosure is opened — collapsing it must never drop the language from
    // the whole-set replace this form eventually submits.
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
    // The Russian title never entered the open form, but the whole set is a
    // replace on the wire — dropping it here would delete it there.
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
