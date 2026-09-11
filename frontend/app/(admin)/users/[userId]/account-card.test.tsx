import { fireEvent, render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { Account, Role } from "@/lib/api/accounts";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { AccountCard } from "./account-card";

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

// The viewer is a different account throughout: `offeredActions` (offered.ts)
// refuses to offer a self-block, and a viewer equal to the account under test
// would hide the very form these tests submit.
const viewerId = "viewer-0000-0000-0000-000000000000";

describe("AccountCard, the block form's reason requirement", () => {
  test("refuses a block submitted with an empty reason, and never disables the button on it", () => {
    render(
      <AccountCard
        account={account()}
        roles={roles}
        viewerId={viewerId}
        dict={en}
        statusChangedAtLabel={null}
      />,
    );

    // No text is typed into the reason field — it starts, and stays, empty.
    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.block }));

    expect(screen.getByRole("alert")).toHaveTextContent(en.errors.reason_required);
    // Refused client-side: nothing here claims the block went through.
    expect(screen.queryByText(en.accounts.card.saved)).not.toBeInTheDocument();
  });
});

describe("AccountCard, the status-explanation panel", () => {
  test("is absent for an account nobody has blocked or deleted", () => {
    render(
      <AccountCard
        account={account({ statusReason: "" })}
        roles={roles}
        viewerId={viewerId}
        dict={en}
        statusChangedAtLabel={null}
      />,
    );

    // An empty statusReason is the signal that nothing needs explaining — the
    // panel must not render as an empty frame around nothing.
    expect(screen.queryByText(en.accounts.card.statusTitle)).not.toBeInTheDocument();
  });

  test("is present and names the actor, for an account that has been blocked", () => {
    render(
      <AccountCard
        account={account({
          status: "blocked",
          statusReason: "cheating in the October contest",
          statusChangedAt: "2026-03-02T09:00:00Z",
          statusChangedBy: "admin-0000-0000-0000-000000000000",
          statusChangedByLogin: "a.admin",
        })}
        roles={roles}
        viewerId={viewerId}
        dict={en}
        statusChangedAtLabel="2 Mar 2026"
      />,
    );

    expect(screen.getByText(en.accounts.card.statusTitle)).toBeInTheDocument();
    expect(screen.getByText("cheating in the October contest")).toBeInTheDocument();
    // The login the server's own query resolved — not a fetch this component
    // makes, and not the unknown-actor fallback.
    expect(screen.getByText(/a\.admin/)).toBeInTheDocument();
    expect(screen.queryByText(en.accounts.card.unknownActor)).not.toBeInTheDocument();
  });

  test("names the actor without a stranded date when the timestamp is missing", () => {
    // A row backfilled with a reason but no status_changed_at: `.replace`
    // used to leave "Changed by X, ." — the sentence must drop its second
    // half instead of the date placeholder.
    render(
      <AccountCard
        account={account({
          status: "blocked",
          statusReason: "backfilled from the old system",
          statusChangedAt: undefined,
          statusChangedBy: "admin-0000-0000-0000-000000000000",
          statusChangedByLogin: "a.admin",
        })}
        roles={roles}
        viewerId={viewerId}
        dict={en}
        statusChangedAtLabel={null}
      />,
    );

    expect(screen.getByText(en.accounts.card.changedByNoDate.replace("{name}", "a.admin"))).toBeInTheDocument();
    expect(screen.queryByText(/,\s*\.$/)).not.toBeInTheDocument();
  });
});

describe("AccountCard, the roles panel", () => {
  // Split from one string: what roles are is an explanation behind the "?";
  // that saving them signs the account out everywhere is a consequence to
  // read before pressing Save, so it stays on screen.
  test("keeps the consequence of saving visible and the explanation closed", () => {
    render(
      <AccountCard
        account={account()}
        roles={roles}
        viewerId={viewerId}
        dict={en}
        statusChangedAtLabel={null}
      />,
    );

    expect(screen.getByText(en.accounts.card.rolesHint)).toBeVisible();
    expect(screen.getByText(en.accounts.card.rolesHelp)).not.toBeVisible();
    expect(screen.getByRole("button", { name: en.chrome.helpLabel })).toHaveAccessibleDescription(
      en.accounts.card.rolesHelp,
    );
  });
});
