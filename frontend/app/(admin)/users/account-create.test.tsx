import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import type { Role } from "@/lib/api/accounts";

// The real Server Actions would pull in Next's server runtime.
const { createAccountAction, importAccountsAction } = vi.hoisted(() => {
  type ActionShape = (previous: unknown, form: FormData) => Promise<Record<string, unknown>>;
  const stub = (): ActionShape =>
    async (previous, form) => {
      void previous;
      void form;
      return {};
    };
  return {
    createAccountAction: vi.fn(stub()),
    importAccountsAction: vi.fn(stub()),
  };
});

vi.mock("./create-actions", () => ({ createAccountAction, importAccountsAction }));

import { AccountCreateControls } from "./account-create";

let en: Dictionary;
let ru: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
  ru = await getDictionary("ru");
});

beforeEach(() => {
  vi.clearAllMocks();
});

const roles: Role[] = [{ code: "student", name: "Student" }];

describe("new account: the one-time password must survive the dialog", () => {
  test("shows the password once the account is created", async () => {
    createAccountAction.mockResolvedValueOnce({
      result: {
        user: { id: "a", login: "s.popescu", fullName: "Sergiu Popescu" },
        one_time_password: "swordfish-1",
      },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.new }));
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.login), "s.popescu");
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.fullName), "Sergiu Popescu");
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.one.submit }));

    expect(await screen.findByText("swordfish-1")).toBeVisible();
    expect(screen.getByText("s.popescu")).toBeVisible();
  });

  test("Escape does not close the dialog once the password is on screen", async () => {
    createAccountAction.mockResolvedValueOnce({
      result: {
        user: { id: "a", login: "s.popescu", fullName: "Sergiu Popescu" },
        one_time_password: "swordfish-1",
      },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.new }));
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.login), "s.popescu");
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.fullName), "Sergiu Popescu");
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.one.submit }));
    await screen.findByText("swordfish-1");

    await userEvent.keyboard("{Escape}");

    expect(screen.getByText("swordfish-1")).toBeVisible();
  });

  test("a click on the backdrop does not close the dialog once the password is on screen", async () => {
    createAccountAction.mockResolvedValueOnce({
      result: {
        user: { id: "a", login: "s.popescu", fullName: "Sergiu Popescu" },
        one_time_password: "swordfish-1",
      },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.new }));
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.login), "s.popescu");
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.fullName), "Sergiu Popescu");
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.one.submit }));
    await screen.findByText("swordfish-1");

    const backdrop = document.querySelector('[data-slot="dialog-backdrop"]');
    expect(backdrop).not.toBeNull();
    await userEvent.click(backdrop as Element);

    expect(screen.getByText("swordfish-1")).toBeVisible();
  });

  test("the corner close control is disabled while the password is on screen", async () => {
    createAccountAction.mockResolvedValueOnce({
      result: {
        user: { id: "a", login: "s.popescu", fullName: "Sergiu Popescu" },
        one_time_password: "swordfish-1",
      },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.new }));
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.login), "s.popescu");
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.fullName), "Sergiu Popescu");
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.one.submit }));
    await screen.findByText("swordfish-1");

    expect(screen.getByRole("button", { name: en.accounts.create.close })).toBeDisabled();
  });

  test("Done closes the dialog", async () => {
    createAccountAction.mockResolvedValueOnce({
      result: {
        user: { id: "a", login: "s.popescu", fullName: "Sergiu Popescu" },
        one_time_password: "swordfish-1",
      },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.new }));
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.login), "s.popescu");
    await userEvent.type(screen.getByLabelText(en.accounts.create.one.fullName), "Sergiu Popescu");
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.one.submit }));
    await screen.findByText("swordfish-1");

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.done }));

    expect(screen.queryByText("swordfish-1")).not.toBeInTheDocument();
  });

  test("a refusal from the server is reported in the administrator's language, not as a generic failure", async () => {
    createAccountAction.mockResolvedValueOnce({ code: "login_taken" });
    render(<AccountCreateControls roles={roles} dict={ru} />);

    await userEvent.click(screen.getByRole("button", { name: ru.accounts.create.new }));
    await userEvent.type(screen.getByLabelText(ru.accounts.create.one.login), "s.popescu");
    await userEvent.type(screen.getByLabelText(ru.accounts.create.one.fullName), "Sergiu Popescu");
    await userEvent.click(screen.getByRole("button", { name: ru.accounts.create.one.submit }));

    expect(await screen.findByText(ru.errors.login_taken)).toBeVisible();
  });
});

describe("import a roster: skipped rows are named, not hidden", () => {
  test("shows a skipped row with its login and a translated reason", async () => {
    importAccountsAction.mockResolvedValueOnce({
      result: { created: [], skipped: [{ login: "i.ivanov", reason: "login_taken" }] },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.import }));
    await userEvent.type(screen.getByLabelText(en.accounts.create.roster.rosterLabel), "i.ivanov, Ivan Ivanov");
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.roster.submit }));

    expect(await screen.findByText("i.ivanov")).toBeVisible();
    expect(screen.getByText(en.accounts.create.roster.reason.login_taken)).toBeVisible();
  });

  test("shows a reason this build does not translate, raw rather than dropped", async () => {
    importAccountsAction.mockResolvedValueOnce({
      result: { created: [], skipped: [{ login: "i.ivanov", reason: "invented_later" }] },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.import }));
    await userEvent.type(screen.getByLabelText(en.accounts.create.roster.rosterLabel), "i.ivanov, Ivan Ivanov");
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.roster.submit }));

    expect(await screen.findByText("invented_later")).toBeVisible();
  });

  test("shows the password issued for each created account", async () => {
    importAccountsAction.mockResolvedValueOnce({
      result: {
        created: [
          { user: { id: "a", login: "s.popescu", fullName: "Sergiu Popescu" }, one_time_password: "swordfish-1" },
        ],
        skipped: [],
      },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.import }));
    await userEvent.type(screen.getByLabelText(en.accounts.create.roster.rosterLabel), "s.popescu, Sergiu Popescu");
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.roster.submit }));

    expect(await screen.findByText("swordfish-1")).toBeVisible();
    expect(screen.getByText("s.popescu")).toBeVisible();
  });

  test("hands over what an import created even when it stopped for load, and names the rows it never reached", async () => {
    importAccountsAction.mockResolvedValueOnce({
      result: {
        created: [
          { user: { id: "a", login: "s.popescu", fullName: "Sergiu Popescu" }, one_time_password: "swordfish-1" },
        ],
        skipped: [],
        not_imported: ["i.ivanov", "a.rusu"],
        stopped: "sign_in_busy",
      },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.import }));
    await userEvent.type(
      screen.getByLabelText(en.accounts.create.roster.rosterLabel),
      "s.popescu, Sergiu Popescu{enter}i.ivanov, Ivan Ivanov{enter}a.rusu, Ana Rusu",
    );
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.roster.submit }));

    expect(await screen.findByText("swordfish-1")).toBeVisible();
    expect(screen.getByText(en.accounts.create.roster.notImported.replace("{n}", "2"))).toBeVisible();
    expect(screen.getByText(en.errors.sign_in_busy)).toBeVisible();
    expect(screen.getByText("i.ivanov")).toBeVisible();
    expect(screen.getByText("a.rusu")).toBeVisible();
  });

  test("Escape does not close the dialog while passwords are on screen", async () => {
    importAccountsAction.mockResolvedValueOnce({
      result: {
        created: [
          { user: { id: "a", login: "s.popescu", fullName: "Sergiu Popescu" }, one_time_password: "swordfish-1" },
        ],
        skipped: [],
      },
    });
    render(<AccountCreateControls roles={roles} dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.import }));
    await userEvent.type(screen.getByLabelText(en.accounts.create.roster.rosterLabel), "s.popescu, Sergiu Popescu");
    await userEvent.click(screen.getByRole("button", { name: en.accounts.create.roster.submit }));
    await screen.findByText("swordfish-1");

    await userEvent.keyboard("{Escape}");

    expect(screen.getByText("swordfish-1")).toBeVisible();
  });
});
