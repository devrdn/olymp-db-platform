import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

// Only the unlock is mocked: it is the one action submitted here.
const unlockSignInAction = vi.hoisted(() => vi.fn());
vi.mock("./actions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./actions")>()),
  unlockSignInAction,
}));

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

// A different viewer, since a self-block form is not offered.
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

    // The reason stays empty.
    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.block }));

    expect(screen.getByRole("alert")).toHaveTextContent(en.errors.reason_required);
    // Refused client-side.
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

    // No status reason, no panel.
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
    // The login the server joined in, not a fallback.
    expect(screen.getByText(/a\.admin/)).toBeInTheDocument();
    expect(screen.queryByText(en.accounts.card.unknownActor)).not.toBeInTheDocument();
  });

  test("names the actor without a stranded date when the timestamp is missing", () => {
    // A backfilled row without a date: the sentence drops its date half.
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
  // The explanation sits behind the "?"; the sign-out consequence stays
  // visible.
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

describe("AccountCard, clearing a sign-in lockout", () => {
  beforeEach(() => {
    unlockSignInAction.mockReset();
    unlockSignInAction.mockResolvedValue({ done: true });
  });

  const renderCard = () =>
    render(
      <AccountCard account={account()} roles={roles} viewerId={viewerId} dict={en} statusChangedAtLabel={null} />,
    );

  test("asks for confirmation before anything is sent", () => {
    renderCard();

    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.unlockSignIn }));

    expect(
      screen.getByText(en.accounts.card.unlockSignInConfirmTitle.replace("{login}", "s.popescu")),
    ).toBeInTheDocument();
    expect(unlockSignInAction).not.toHaveBeenCalled();
  });

  test("sends nothing when the confirmation is cancelled", async () => {
    renderCard();

    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.unlockSignIn }));
    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.unlockSignInCancel }));

    await waitFor(() =>
      expect(
        screen.queryByText(en.accounts.card.unlockSignInConfirmTitle.replace("{login}", "s.popescu")),
      ).not.toBeInTheDocument(),
    );
    expect(unlockSignInAction).not.toHaveBeenCalled();
  });

  test("clears the lockout for this account once confirmed, and says so", async () => {
    renderCard();

    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.unlockSignIn }));
    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.unlockSignInConfirm }));

    await waitFor(() => expect(unlockSignInAction).toHaveBeenCalledTimes(1));
    const submitted = unlockSignInAction.mock.calls[0][1] as FormData;
    expect(submitted.get("userId")).toBe(account().id);
    expect(await screen.findByText(en.accounts.card.unlocked)).toBeInTheDocument();
  });
});
