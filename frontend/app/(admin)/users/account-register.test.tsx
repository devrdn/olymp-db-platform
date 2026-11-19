import type { ReactElement } from "react";
import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { Account, Role } from "@/lib/api/accounts";
import { formatMoment } from "@/lib/format/datetime";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { AccountRegister } from "./account-register";
import { SelectionProvider } from "./selection";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

// The checkboxes read the selection store from context, so every render needs a
// provider, as `layout.tsx` supplies in the app.
function renderRegister(ui: ReactElement) {
  return render(<SelectionProvider>{ui}</SelectionProvider>);
}

const account = (over: Partial<Account> = {}): Account => ({
  id: "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6",
  login: "s.popescu",
  email: undefined,
  fullName: "Sergiu Popescu",
  status: "active",
  roles: ["student"],
  mustChangePassword: false,
  lastLoginAt: "2026-03-01T10:00:00Z",
  createdAt: "2026-02-01T10:00:00Z",
  statusReason: "",
  statusChangedAt: undefined,
  statusChangedBy: undefined,
  statusChangedByLogin: "",
  ...over,
});

const roles: Role[] = [
  { code: "student", name: "Student" },
  { code: "admin", name: "System administrator" },
];

const props = {
  total: 1,
  offset: 0,
  pageHref: (offset: number) => `/users?offset=${offset}`,
  filtered: false,
  roles,
  locale: "en" as const,
  mail: { status: "off" } as const,
};

describe("AccountRegister", () => {
  test("names the account and the person behind it", () => {
    renderRegister(<AccountRegister accounts={[account()]} {...props} dict={en} />);

    const row = screen.getAllByRole("row")[1];
    expect(within(row).getByText("Sergiu Popescu")).toBeInTheDocument();
    expect(within(row).getByText("s.popescu")).toBeInTheDocument();
  });

  test("shows a role by the name a person reads, not by its code", () => {
    // Roles are shown by name, not code.
    renderRegister(<AccountRegister accounts={[account({ roles: ["admin"] })]} {...props} dict={en} />);

    expect(screen.getByText("System administrator")).toBeInTheDocument();
  });

  test("falls back to the code for a role the catalogue does not name", () => {
    // An unknown role is shown raw; an empty cell would deny it.
    renderRegister(<AccountRegister accounts={[account({ roles: ["dean"] })]} {...props} dict={en} />);

    expect(screen.getByText("dean")).toBeInTheDocument();
  });

  test("says an account holds no role rather than leaving the cell blank", () => {
    // No roles is a real state and is stated.
    renderRegister(<AccountRegister accounts={[account({ roles: [] })]} {...props} dict={en} />);

    expect(screen.getByText(en.accounts.noRoles)).toBeInTheDocument();
  });

  test("marks an account still carrying the password it was handed", () => {
    // Shows who still carries an issued password.
    renderRegister(
      <AccountRegister accounts={[account({ mustChangePassword: true })]} {...props} dict={en} />,
    );

    expect(screen.getByText(en.accounts.handoverPending)).toBeInTheDocument();
  });

  test("says an account has never signed in instead of showing an empty cell", () => {
    renderRegister(<AccountRegister accounts={[account({ lastLoginAt: undefined })]} {...props} dict={en} />);

    expect(screen.getByText(en.accounts.never)).toBeInTheDocument();
  });

  test("offers the way out of a filter that matched nothing", () => {
    renderRegister(<AccountRegister accounts={[]} {...props} filtered dict={en} />);

    expect(screen.getByText(en.accounts.empty.title)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.accounts.empty.reset })).toHaveAttribute(
      "href",
      "/users",
    );
  });

  test("does not offer a reset when nothing has been created at all", () => {
    // No filter, so no reset.
    renderRegister(<AccountRegister accounts={[]} {...props} dict={en} />);

    expect(screen.getByText(en.accounts.emptyAll.title)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en.accounts.empty.reset })).not.toBeInTheDocument();
  });

  test("pages forward only while there is more to see", () => {
    const many = Array.from({ length: 50 }, (_, i) =>
      account({ id: `id-${i}`, login: `user${i}` }),
    );

    const { rerender } = renderRegister(
      <AccountRegister accounts={many} {...props} total={120} dict={en} />,
    );
    expect(screen.getByRole("link", { name: en.accounts.olderPage })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en.accounts.newerPage })).not.toBeInTheDocument();

    // `rerender` replaces the tree, so the provider is supplied again.
    rerender(
      <SelectionProvider>
        <AccountRegister accounts={many} {...props} total={120} offset={100} dict={en} />
      </SelectionProvider>,
    );
    expect(screen.getByRole("link", { name: en.accounts.newerPage })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en.accounts.olderPage })).not.toBeInTheDocument();
  });
});

/** SPEC.md §5: the register loses columns, not rows. */
describe("AccountRegister below a desktop's width", () => {
  const cellsOf = () => within(screen.getAllByRole("row")[1]).getAllByRole("cell");

  test("folds the roles and the last sign-in into a caption under the account until wide", () => {
    renderRegister(<AccountRegister accounts={[account()]} {...props} dict={en} />);

    for (const name of [en.accounts.columns.roles, en.accounts.columns.lastSeen]) {
      expect(screen.getByRole("columnheader", { name })).toHaveClass("max-wide:hidden");
    }
    // A cell that kept its column while the header went would shift the row.
    const [, , , , rolesCell, lastSeenCell] = cellsOf();
    expect(rolesCell).toHaveClass("max-wide:hidden");
    expect(lastSeenCell).toHaveClass("max-wide:hidden");

    const caption = screen.getByText(
      `Student · ${formatMoment("2026-03-01T10:00:00Z", { locale: "en" })}`,
    );
    expect(caption).toHaveClass("hidden", "max-wide:block");
  });

  test("says in the caption, too, that there is no role and no sign-in", () => {
    renderRegister(
      <AccountRegister accounts={[account({ roles: [], lastLoginAt: undefined })]} {...props} dict={en} />,
    );

    expect(screen.getByText(`${en.accounts.noRoles} · ${en.accounts.never}`)).toHaveClass(
      "hidden",
      "max-wide:block",
    );
  });

  test("drops the row number on a phone, where the name needs the room", () => {
    renderRegister(<AccountRegister accounts={[account()]} {...props} dict={en} />);

    expect(screen.getByRole("columnheader", { name: en.accounts.columns.index })).toHaveClass(
      "max-narrow:hidden",
    );
    expect(cellsOf()[1]).toHaveClass("max-narrow:hidden");
  });

  test("gives the account the room on a phone: half the side padding, the state its narrowest", () => {
    renderRegister(<AccountRegister accounts={[account()]} {...props} dict={en} />);

    expect(screen.getByRole("table")).toHaveClass("max-narrow:[--row-px:--spacing(2)]");
    expect(screen.getByRole("columnheader", { name: en.accounts.columns.state })).toHaveClass(
      "max-narrow:w-px",
    );
  });

  test("keeps the account and its state at every width", () => {
    renderRegister(<AccountRegister accounts={[account()]} {...props} dict={en} />);

    for (const name of [en.accounts.columns.account, en.accounts.columns.state]) {
      expect(screen.getByRole("columnheader", { name }).className).not.toMatch(/hidden/);
    }
    const [, , accountCell, stateCell] = cellsOf();
    expect(accountCell.className).not.toMatch(/hidden/);
    expect(stateCell.className).not.toMatch(/hidden/);
  });

  test("scrolls in its own box, never the page", () => {
    renderRegister(<AccountRegister accounts={[account()]} {...props} dict={en} />);

    expect(screen.getByRole("table").parentElement).toHaveClass("relative", "overflow-x-auto");
  });
});
