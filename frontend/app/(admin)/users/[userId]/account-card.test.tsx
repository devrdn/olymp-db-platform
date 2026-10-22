import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

// Only the unlock and the reset are mocked: they are the actions submitted here.
const { unlockSignInAction, resetPasswordAction } = vi.hoisted(() => ({
  unlockSignInAction: vi.fn(),
  resetPasswordAction: vi.fn(),
}));
vi.mock("./actions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./actions")>()),
  unlockSignInAction,
  resetPasswordAction,
}));

import type { Account, Role } from "@/lib/api/accounts";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import type { MailOffer } from "../email-delivery";
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
        locale="en"
        mail={{ status: "off" }}
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
        locale="en"
        mail={{ status: "off" }}
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
        locale="en"
        mail={{ status: "off" }}
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
        locale="en"
        mail={{ status: "off" }}
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
        locale="en"
        mail={{ status: "off" }}
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
      <AccountCard account={account()} roles={roles} viewerId={viewerId} dict={en} statusChangedAtLabel={null}
        locale="en"
        mail={{ status: "off" }} />,
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

describe("AccountCard, a password reset with the sign-in details by email", () => {
  beforeEach(() => {
    resetPasswordAction.mockReset();
  });

  const on: MailOffer = { status: "on", perMinute: 25 };
  const reachable = account({ email: "s@example.edu" });

  const renderCard = (mail: MailOffer, subject: Account = reachable, dict: Dictionary = en) =>
    render(
      <AccountCard
        account={subject}
        roles={roles}
        viewerId={viewerId}
        dict={dict}
        statusChangedAtLabel={null}
        locale={dict === en ? "en" : "ro"}
        mail={mail}
      />,
    );

  test("offers no email option while mail is off, and sends none", async () => {
    resetPasswordAction.mockResolvedValue({ done: true, oneTimePassword: "swordfish-1", emailed: false });
    renderCard({ status: "off" });

    expect(screen.queryByRole("checkbox", { name: en.accounts.signInMail.option })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.resetPassword }));

    expect(await screen.findByText("swordfish-1")).toBeInTheDocument();
    const submitted = resetPasswordAction.mock.calls[0][1] as FormData;
    expect(submitted.has("send_by_email")).toBe(false);
    // Not asked, so nothing is said about email.
    expect(screen.queryByText(en.accounts.signInMail.notEmailed.replace("{n}", "1"))).not.toBeInTheDocument();
  });

  test("ticks the option by default, sends it, and shows the password, the queued email and when it goes", async () => {
    resetPasswordAction.mockResolvedValue({ done: true, oneTimePassword: "swordfish-1", emailed: true });
    renderCard(on);

    expect(screen.getByRole("checkbox", { name: en.accounts.signInMail.option })).toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.resetPassword }));

    expect(await screen.findByText("swordfish-1")).toBeInTheDocument();
    const submitted = resetPasswordAction.mock.calls[0][1] as FormData;
    expect(submitted.get("send_by_email")).toBe("on");
    expect(submitted.get("userId")).toBe(reachable.id);
    expect(screen.getByText("1 sign-in email queued.")).toBeInTheDocument();
    expect(screen.getByText(/It should go out within about 1 minute/)).toBeInTheDocument();
  });

  test("offers no email option for an account that is not active", () => {
    renderCard(on, account({ email: "s@example.edu", status: "blocked" }));

    expect(screen.getByRole("button", { name: en.accounts.card.resetPassword })).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: en.accounts.signInMail.option })).not.toBeInTheDocument();
  });

  test("offers no email option for an account without an address", () => {
    renderCard(on, account({ email: undefined }));

    expect(screen.getByRole("button", { name: en.accounts.card.resetPassword })).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: en.accounts.signInMail.option })).not.toBeInTheDocument();
  });

  test("says it could not check whether email is on, instead of hiding the option", () => {
    renderCard({ status: "unknown" });

    expect(screen.getByText(en.accounts.signInMail.unknown)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: en.accounts.signInMail.option })).not.toBeInTheDocument();
  });

  // The address can be removed between the page and the reset; the server's
  // answer is what counts.
  test("names why the password was not emailed, and still shows it", async () => {
    resetPasswordAction.mockResolvedValue({
      done: true,
      oneTimePassword: "swordfish-1",
      emailed: false,
      emailSkippedReason: "no_address",
    });
    renderCard(on);

    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.resetPassword }));

    expect(await screen.findByText("swordfish-1")).toBeInTheDocument();
    expect(
      screen.getByText(
        en.accounts.signInMail.group.replace("{n}", "1").replace("{reason}", en.accounts.emailSkipped.no_address),
      ),
    ).toBeInTheDocument();
  });

  test("reports mail turned off since the page loaded in the administrator's language", async () => {
    resetPasswordAction.mockResolvedValue({ code: "mail_off" });
    const ro = await getDictionary("ro");
    renderCard(on, reachable, ro);

    fireEvent.click(screen.getByRole("button", { name: ro.accounts.card.resetPassword }));

    expect(await screen.findByRole("alert")).toHaveTextContent(ro.errors.mail_off);
  });
});
