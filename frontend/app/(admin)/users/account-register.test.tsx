import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { Account, Role } from "@/lib/api/accounts";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { AccountRegister } from "./account-register";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

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
};

describe("AccountRegister", () => {
  test("names the account and the person behind it", () => {
    render(<AccountRegister accounts={[account()]} {...props} dict={en} />);

    const row = screen.getAllByRole("row")[1];
    expect(within(row).getByText("Sergiu Popescu")).toBeInTheDocument();
    expect(within(row).getByText("s.popescu")).toBeInTheDocument();
  });

  test("shows a role by the name a person reads, not by its code", () => {
    // The codes are what authorisation works in. An administrator picking who
    // may do what should not have to know that "admin" is spelled that way.
    render(<AccountRegister accounts={[account({ roles: ["admin"] })]} {...props} dict={en} />);

    expect(screen.getByText("System administrator")).toBeInTheDocument();
  });

  test("falls back to the code for a role the catalogue does not name", () => {
    // A role added to the table while this page was open. Showing nothing
    // would say the account holds no role, which is a different and wrong fact.
    render(<AccountRegister accounts={[account({ roles: ["dean"] })]} {...props} dict={en} />);

    expect(screen.getByText("dean")).toBeInTheDocument();
  });

  test("says an account holds no role rather than leaving the cell blank", () => {
    // An empty cell reads as missing data. No roles at all is a real state and
    // worth stating — such an account can sign in and do nothing.
    render(<AccountRegister accounts={[account({ roles: [] })]} {...props} dict={en} />);

    expect(screen.getByText(en.accounts.noRoles)).toBeInTheDocument();
  });

  test("marks an account still carrying the password it was handed", () => {
    // The administrator who reset it needs to see who has not yet picked their
    // own — that is the difference between "handed over" and "in use".
    render(
      <AccountRegister accounts={[account({ mustChangePassword: true })]} {...props} dict={en} />,
    );

    expect(screen.getByText(en.accounts.handoverPending)).toBeInTheDocument();
  });

  test("says an account has never signed in instead of showing an empty cell", () => {
    render(<AccountRegister accounts={[account({ lastLoginAt: undefined })]} {...props} dict={en} />);

    expect(screen.getByText(en.accounts.never)).toBeInTheDocument();
  });

  test("offers the way out of a filter that matched nothing", () => {
    render(<AccountRegister accounts={[]} {...props} filtered dict={en} />);

    expect(screen.getByText(en.accounts.empty.title)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.accounts.empty.reset })).toHaveAttribute(
      "href",
      "/users",
    );
  });

  test("does not offer a reset when nothing has been created at all", () => {
    // There is no filter to clear, and a control that cannot help suggests the
    // emptiness is the reader's doing.
    render(<AccountRegister accounts={[]} {...props} dict={en} />);

    expect(screen.getByText(en.accounts.emptyAll.title)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en.accounts.empty.reset })).not.toBeInTheDocument();
  });

  test("pages forward only while there is more to see", () => {
    const many = Array.from({ length: 50 }, (_, i) =>
      account({ id: `id-${i}`, login: `user${i}` }),
    );

    const { rerender } = render(
      <AccountRegister accounts={many} {...props} total={120} dict={en} />,
    );
    expect(screen.getByRole("link", { name: en.accounts.olderPage })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en.accounts.newerPage })).not.toBeInTheDocument();

    rerender(<AccountRegister accounts={many} {...props} total={120} offset={100} dict={en} />);
    expect(screen.getByRole("link", { name: en.accounts.newerPage })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en.accounts.olderPage })).not.toBeInTheDocument();
  });
});
