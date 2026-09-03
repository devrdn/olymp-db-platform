import { Profiler } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import { MAX_BULK_ACCOUNTS } from "@/lib/api/accounts-terms";
import type { Role } from "@/lib/api/accounts";

// The bulk actions are Server Actions ("use server"): importing the real
// module for a component test would pull in Next's server runtime. The
// module boundary is what gets faked, the same way change-password-form.test.tsx
// fakes its own actions module.
// Typed as `(previous, form) => Promise<...>` — the same shape every bulk
// action has — purely so `.mock.calls[0][1]` below is a `FormData` and not
// `undefined`; no test relies on a default resolution, every one that reads
// the result calls `mockResolvedValueOnce` first. Built inside one
// `vi.hoisted` block: everything it needs has to be declared in the same
// hoisted call, since hoisting moves the call itself but not ordinary code
// around it.
const {
  bulkBlockAction,
  bulkUnblockAction,
  bulkDeleteAction,
  bulkReplaceRolesAction,
  bulkResetPasswordAction,
} = vi.hoisted(() => {
  type BulkActionShape = (previous: unknown, form: FormData) => Promise<Record<string, unknown>>;
  const stub = (): BulkActionShape =>
    async (previous, form) => {
      void previous;
      void form;
      return {};
    };

  return {
    bulkBlockAction: vi.fn(stub()),
    bulkUnblockAction: vi.fn(stub()),
    bulkDeleteAction: vi.fn(stub()),
    bulkReplaceRolesAction: vi.fn(stub()),
    bulkResetPasswordAction: vi.fn(stub()),
  };
});

vi.mock("./bulk-actions", () => ({
  bulkBlockAction,
  bulkUnblockAction,
  bulkDeleteAction,
  bulkReplaceRolesAction,
  bulkResetPasswordAction,
}));

import {
  RowCheckbox,
  SelectAllCheckbox,
  SelectionBar,
  SelectionProvider,
  useSelectedIds,
} from "./selection";

let en: Dictionary;
let ru: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
  ru = await getDictionary("ru");
});

// The bulk action mocks are module-level `vi.fn()`s shared by every test in
// this file; without clearing their call history between tests, a test that
// asserts `not.toHaveBeenCalled()` would see calls made by an earlier test.
// This only clears `.mock.calls` and friends, not a mock's implementation, so
// the default stub each one carries (an empty successful result) survives.
beforeEach(() => {
  vi.clearAllMocks();
});

/** Renders the bar with a selection already picked, in the Russian locale the
 * brief's own wording (заблокировать, подтвердить, укажите причину) is
 * written against. */
async function renderBar({
  selected = [],
  roles = [],
}: {
  selected?: string[];
  roles?: Role[];
} = {}) {
  render(
    <SelectionProvider>
      {selected.map((id) => (
        <RowCheckbox key={id} id={id} label={id} />
      ))}
      <SelectionBar dict={ru} roles={roles} />
    </SelectionProvider>,
  );

  for (const id of selected) {
    await userEvent.click(screen.getByRole("checkbox", { name: id }));
  }
}

describe("selection store", () => {
  function Counted({ id, onRender }: { id: string; onRender: () => void }) {
    return (
      <Profiler id={id} onRender={onRender}>
        <RowCheckbox id={id} label={id} />
      </Profiler>
    );
  }

  test("selecting a row re-renders that row and not its neighbours", async () => {
    const renders = { first: 0, second: 0 };
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
        <SelectionBar dict={en} roles={[]} />
      </SelectionProvider>,
    );

    expect(screen.queryByText(/selected/)).not.toBeInTheDocument();
  });

  test("says how many accounts are picked, and clears them on request", async () => {
    render(
      <SelectionProvider>
        <RowCheckbox id="a" label="a" />
        <RowCheckbox id="b" label="b" />
        <SelectionBar dict={en} roles={[]} />
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

  test("does not offer to send more than the backend will accept", async () => {
    const many = Array.from({ length: MAX_BULK_ACCOUNTS + 1 }, (_, i) => `id-${i}`);
    render(
      <SelectionProvider>
        <SelectAllCheckbox ids={many} label="page" />
        <SelectionBar dict={ru} roles={[]} />
      </SelectionProvider>,
    );

    await userEvent.click(screen.getByRole("checkbox", { name: "page" }));

    expect(screen.getByRole("button", { name: ru.accounts.selection.bulk.block })).toBeDisabled();
    expect(
      screen.getByText(ru.accounts.selection.bulk.tooMany.replace("{n}", String(MAX_BULK_ACCOUNTS))),
    ).toBeVisible();
  });
});

describe("bulk actions: block and delete require a reason", () => {
  test("refuses to send a block without a reason", async () => {
    await renderBar({ selected: ["a", "b"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.block }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));

    expect(bulkBlockAction).not.toHaveBeenCalled();
    expect(screen.getByText(/укажите причину/i)).toBeVisible();
  });

  test("a reason that is only whitespace is refused the same way", async () => {
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.block }));
    await userEvent.type(
      screen.getByLabelText(ru.accounts.selection.bulk.reasonLabel),
      "   ",
    );
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));

    expect(bulkBlockAction).not.toHaveBeenCalled();
    expect(screen.getByText(/укажите причину/i)).toBeVisible();
  });

  test("refuses to send a delete without a reason", async () => {
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.delete }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));

    expect(bulkDeleteAction).not.toHaveBeenCalled();
    expect(screen.getByText(/укажите причину/i)).toBeVisible();
  });

  test("a filled-in reason is sent", async () => {
    bulkBlockAction.mockResolvedValueOnce({ result: { changed: ["a"], skipped: [] } });
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.block }));
    await userEvent.type(screen.getByLabelText(ru.accounts.selection.bulk.reasonLabel), "cheating");
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));

    expect(await screen.findByText(ru.accounts.selection.bulk.changed.replace("{n}", "1"))).toBeVisible();
    const form = bulkBlockAction.mock.calls[0]![1] as FormData;
    expect(form.get("reason")).toBe("cheating");
    expect(form.getAll("ids")).toEqual(["a"]);
  });

  test("unblock needs no reason", async () => {
    bulkUnblockAction.mockResolvedValueOnce({ result: { changed: ["a"], skipped: [] } });
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.unblock }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));

    expect(bulkUnblockAction).toHaveBeenCalled();
  });
});

describe("bulk outcome: what did not change is never hidden", () => {
  test("names the accounts it did not touch, with a translated reason", async () => {
    bulkBlockAction.mockResolvedValueOnce({
      result: { changed: [], skipped: [{ id: "a", login: "ivanov", reason: "last_administrator" }] },
    });
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.block }));
    await userEvent.type(screen.getByLabelText(ru.accounts.selection.bulk.reasonLabel), "reason");
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));

    expect(await screen.findByText(/ivanov/)).toBeVisible();
    expect(screen.getByText(/последний администратор/i)).toBeVisible();
  });

  test("shows a reason this build does not translate, raw rather than dropped", async () => {
    bulkBlockAction.mockResolvedValueOnce({
      result: { changed: [], skipped: [{ id: "a", login: "petrov", reason: "quarantined" }] },
    });
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.block }));
    await userEvent.type(screen.getByLabelText(ru.accounts.selection.bulk.reasonLabel), "reason");
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));

    expect(await screen.findByText(/petrov/)).toBeVisible();
    expect(screen.getByText("quarantined")).toBeVisible();
  });

  test("an account with no login on record is still named, by its id", async () => {
    bulkBlockAction.mockResolvedValueOnce({
      result: { changed: [], skipped: [{ id: "ghost-id", login: "", reason: "not_found" }] },
    });
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.block }));
    await userEvent.type(screen.getByLabelText(ru.accounts.selection.bulk.reasonLabel), "reason");
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));

    expect(await screen.findByText(/ghost-id/)).toBeVisible();
  });
});

describe("bulk actions: roles and password reset", () => {
  test("the roles dialog replaces the role set with exactly what is checked", async () => {
    bulkReplaceRolesAction.mockResolvedValueOnce({ result: { changed: ["a"], skipped: [] } });
    const roles: Role[] = [
      { code: "admin", name: "Administrator" },
      { code: "judge", name: "Judge" },
    ];
    await renderBar({ selected: ["a"], roles });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.roles }));
    await userEvent.click(screen.getByRole("checkbox", { name: /Administrator/ }));
    await userEvent.click(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.rolesDialog.submit }),
    );

    expect(bulkReplaceRolesAction).toHaveBeenCalled();
    const form = bulkReplaceRolesAction.mock.calls[0]![1] as FormData;
    expect(form.getAll("roles")).toEqual(["admin"]);
    expect(form.getAll("ids")).toEqual(["a"]);
  });

  test("resetting passwords shows the issued password for each account", async () => {
    bulkResetPasswordAction.mockResolvedValueOnce({
      result: { issued: [{ id: "a", login: "ivanov", oneTimePassword: "swordfish-1" }], skipped: [] },
    });
    await renderBar({ selected: ["a"] });

    await userEvent.click(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.resetPassword }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.resetDialog.submit }),
    );

    expect(await screen.findByText("swordfish-1")).toBeVisible();
    expect(screen.getByText("ivanov")).toBeVisible();
  });
});

describe("bulk actions: an empty roles submit needs a second, explicit step", () => {
  test("does not reach the action when nothing is checked, and names the count", async () => {
    const roles: Role[] = [{ code: "admin", name: "Administrator" }];
    await renderBar({ selected: ["a", "b"], roles });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.roles }));
    await userEvent.click(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.rolesDialog.submit }),
    );

    expect(bulkReplaceRolesAction).not.toHaveBeenCalled();
    expect(
      screen.getByText(ru.accounts.selection.bulk.rolesDialog.confirmEmptyTitle),
    ).toBeVisible();
    expect(
      screen.getByText(ru.accounts.selection.bulk.rolesDialog.confirmEmptyBody.replace("{n}", "2")),
    ).toBeVisible();
  });

  test("submits the empty set only after the explicit confirmation", async () => {
    bulkReplaceRolesAction.mockResolvedValueOnce({ result: { changed: ["a"], skipped: [] } });
    const roles: Role[] = [{ code: "admin", name: "Administrator" }];
    await renderBar({ selected: ["a"], roles });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.roles }));
    await userEvent.click(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.rolesDialog.submit }),
    );
    expect(bulkReplaceRolesAction).not.toHaveBeenCalled();

    await userEvent.click(
      screen.getByRole("button", {
        name: ru.accounts.selection.bulk.rolesDialog.confirmEmptySubmit,
      }),
    );

    expect(bulkReplaceRolesAction).toHaveBeenCalled();
    const form = bulkReplaceRolesAction.mock.calls[0]![1] as FormData;
    expect(form.getAll("roles")).toEqual([]);
    expect(form.getAll("ids")).toEqual(["a"]);
  });

  test("going back from the confirmation returns to the checkbox list without submitting", async () => {
    const roles: Role[] = [{ code: "admin", name: "Administrator" }];
    await renderBar({ selected: ["a"], roles });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.roles }));
    await userEvent.click(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.rolesDialog.submit }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.rolesDialog.back }),
    );

    expect(bulkReplaceRolesAction).not.toHaveBeenCalled();
    expect(screen.getByRole("checkbox", { name: /Administrator/ })).toBeVisible();
  });

  // Already covered by "the roles dialog replaces the role set with exactly
  // what is checked" above: ticking at least one role still submits in a
  // single step, with no confirmation screen in between.
});

describe("bulk dialogs: dismissal is blocked while pending or holding a result", () => {
  test("Escape closes the dialog before anything has been submitted", async () => {
    await renderBar({ selected: ["a"] });
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.unblock }));
    expect(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm })).toBeVisible();

    await userEvent.keyboard("{Escape}");

    expect(
      screen.queryByRole("button", { name: ru.accounts.selection.bulk.confirm }),
    ).not.toBeInTheDocument();
  });

  test("Escape does not close the dialog while the request is pending", async () => {
    // Held open under this test's own control rather than left to hang
    // forever: an action promise that never settles leaves React's own
    // pending-transition bookkeeping stuck, which then bleeds into whichever
    // test runs next against the same mock. Resolving it before this test
    // ends (below) keeps the test isolated.
    let resolveAction: (value: { code?: string; result?: unknown }) => void = () => {};
    bulkUnblockAction.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveAction = resolve;
        }),
    );
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.unblock }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));
    await screen.findByRole("button", { name: ru.accounts.selection.bulk.submitting });

    await userEvent.keyboard("{Escape}");

    expect(screen.getByRole("button", { name: ru.accounts.selection.bulk.submitting })).toBeVisible();
    expect(screen.getByRole("button", { name: ru.accounts.selection.bulk.close })).toBeDisabled();

    resolveAction({ result: { changed: ["a"], skipped: [] } });
    await screen.findByText(ru.accounts.selection.bulk.changed.replace("{n}", "1"));
  });

  test("Escape does not close the dialog while a result is on screen", async () => {
    bulkUnblockAction.mockResolvedValueOnce({ result: { changed: ["a"], skipped: [] } });
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.unblock }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));
    await screen.findByText(ru.accounts.selection.bulk.changed.replace("{n}", "1"));

    await userEvent.keyboard("{Escape}");

    expect(screen.getByText(ru.accounts.selection.bulk.changed.replace("{n}", "1"))).toBeVisible();
    expect(screen.getByRole("button", { name: ru.accounts.selection.bulk.close })).toBeDisabled();
  });

  test("the explicit Done button still closes the dialog once a result is shown", async () => {
    bulkUnblockAction.mockResolvedValueOnce({ result: { changed: ["a"], skipped: [] } });
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.unblock }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));
    await screen.findByText(ru.accounts.selection.bulk.changed.replace("{n}", "1"));

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.done }));

    expect(
      screen.queryByText(ru.accounts.selection.bulk.changed.replace("{n}", "1")),
    ).not.toBeInTheDocument();
  });
});

describe("bulk dialog: the corner control reads as close, not cancel", () => {
  test("names the corner X 'close' rather than reusing the cancel wording", async () => {
    await renderBar({ selected: ["a"] });
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.block }));

    // The footer's own Cancel button keeps its own label; the corner control
    // is a distinct, generic "close" — not the same string as either.
    expect(screen.getByRole("button", { name: ru.accounts.selection.bulk.close })).toBeVisible();
    expect(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.cancel }),
    ).toBeVisible();
  });
});

describe("bulk actions: the reason error clears as soon as it is corrected", () => {
  test("typing a real reason clears the red line without a second submit attempt", async () => {
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.block }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));
    const field = screen.getByLabelText(ru.accounts.selection.bulk.reasonLabel);
    expect(screen.getByText(/укажите причину/i)).toBeVisible();
    expect(field).toHaveAttribute("aria-invalid", "true");

    await userEvent.type(field, "cheating");

    expect(screen.queryByText(/укажите причину/i)).not.toBeInTheDocument();
    expect(field).not.toHaveAttribute("aria-invalid");
  });

  test("a field left whitespace-only does not clear the error", async () => {
    await renderBar({ selected: ["a"] });

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.block }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));
    await userEvent.type(screen.getByLabelText(ru.accounts.selection.bulk.reasonLabel), "   ");

    expect(screen.getByText(/укажите причину/i)).toBeVisible();
  });
});

describe("bulk outcome: the selection only clears after a run that changed something", () => {
  test("clears the selection once Done is pressed after a run that changed an account", async () => {
    bulkUnblockAction.mockResolvedValueOnce({ result: { changed: ["a"], skipped: [] } });
    render(
      <SelectionProvider>
        <RowCheckbox id="a" label="a" />
        <SelectionBar dict={ru} roles={[]} />
      </SelectionProvider>,
    );
    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.unblock }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));
    await screen.findByText(ru.accounts.selection.bulk.changed.replace("{n}", "1"));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.done }));

    expect(screen.getByRole("checkbox", { name: "a" })).not.toBeChecked();
    expect(screen.queryByText(/выбран/)).not.toBeInTheDocument();
  });

  test("keeps the selection when a run changed nothing, so it can be corrected and retried", async () => {
    bulkUnblockAction.mockResolvedValueOnce({
      result: { changed: [], skipped: [{ id: "a", login: "ivanov", reason: "already_in_status" }] },
    });
    render(
      <SelectionProvider>
        <RowCheckbox id="a" label="a" />
        <SelectionBar dict={ru} roles={[]} />
      </SelectionProvider>,
    );
    await userEvent.click(screen.getByRole("checkbox", { name: "a" }));

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.unblock }));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.confirm }));
    await screen.findByText(ru.accounts.selection.bulk.changed.replace("{n}", "0"));
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.selection.bulk.done }));

    expect(screen.getByRole("checkbox", { name: "a" })).toBeChecked();
  });
});

describe("bulk password reset: an empty result says so", () => {
  test("says plainly that nothing was reset when there is nothing to show", async () => {
    bulkResetPasswordAction.mockResolvedValueOnce({ result: { issued: [], skipped: [] } });
    await renderBar({ selected: ["a"] });

    await userEvent.click(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.resetPassword }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: ru.accounts.selection.bulk.resetDialog.submit }),
    );

    expect(await screen.findByText(ru.accounts.selection.bulk.resetDialog.none)).toBeVisible();
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
