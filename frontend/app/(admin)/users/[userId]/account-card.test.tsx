import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

// Only the actions submitted here are mocked.
const { unlockSignInAction, resetPasswordAction, unlinkIdentityAction, allowIdentityAction } = vi.hoisted(() => ({
  unlockSignInAction: vi.fn(),
  resetPasswordAction: vi.fn(),
  unlinkIdentityAction: vi.fn(),
  allowIdentityAction: vi.fn(),
}));
vi.mock("./actions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./actions")>()),
  unlockSignInAction,
  resetPasswordAction,
  unlinkIdentityAction,
  allowIdentityAction,
}));
// The reload under a refusal re-reads the page through the router.
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: vi.fn() }) }));

import type { Account, IdentityList, LinkedIdentity, Role, UnlinkedIdentity } from "@/lib/api/accounts";
import { formatMoment } from "@/lib/format/datetime";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import { fill, isolate } from "@/lib/i18n/fill";

import type { MailOffer } from "../email-delivery";
import { AccountCard } from "./account-card";
import { answerYesOnFakeClock, flushFakeClock, onFakeClock } from "@/components/ui/confirm-dialog-test-support";

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

const noIdentities: IdentityList = { hasPassword: true, items: [], unlinked: [] };

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
        identities={noIdentities}
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
        identities={noIdentities}
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
        identities={noIdentities}
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
        identities={noIdentities}
        mail={{ status: "off" }}
      />,
    );

    expect(screen.getByText(fill(en.accounts.card.changedByNoDate, { name: "a.admin" }))).toBeInTheDocument();
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
        identities={noIdentities}
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
    unlockSignInAction.mockResolvedValue({ ok: true });
  });

  const renderCard = () =>
    render(
      <AccountCard
        account={account()}
        roles={roles}
        identities={noIdentities}
        viewerId={viewerId}
        dict={en}
        statusChangedAtLabel={null}
        locale="en"
        mail={{ status: "off" }}
      />,
    );

  test("asks first, naming the login, and sends nothing when the question is cancelled", async () => {
    renderCard();

    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.unlockSignIn }));

    const dialog = await screen.findByRole("dialog", {
      name: fill(en.accounts.card.unlockSignInConfirmTitle, { login: isolate("s.popescu") }),
    });
    expect(dialog).toHaveTextContent(en.accounts.card.unlockSignInConfirmBody);
    fireEvent.click(screen.getAllByRole("button", { name: en.accounts.card.unlockSignInCancel })[0]);

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(unlockSignInAction).not.toHaveBeenCalled();
  });

  test("clears the lockout for this account once confirmed, and says so", () =>
    onFakeClock(async () => {
      renderCard();

      fireEvent.click(screen.getByRole("button", { name: en.accounts.card.unlockSignIn }));
      await answerYesOnFakeClock(en.accounts.card.unlockSignInConfirm);

      expect(unlockSignInAction).toHaveBeenCalledTimes(1);
      const submitted = unlockSignInAction.mock.calls[0][1] as FormData;
      expect(submitted.get("userId")).toBe(account().id);
      expect(screen.getByText(en.accounts.card.unlocked)).toBeInTheDocument();
    }));
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
        identities={noIdentities}
        mail={mail}
      />,
    );

  test("offers no email option while mail is off, and sends none", async () => {
    resetPasswordAction.mockResolvedValue({ ok: true, oneTimePassword: "swordfish-1", emailed: false });
    renderCard({ status: "off" });

    expect(screen.queryByRole("checkbox", { name: en.accounts.signInMail.option })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.resetPassword }));

    expect(await screen.findByText("swordfish-1")).toBeInTheDocument();
    const submitted = resetPasswordAction.mock.calls[0][1] as FormData;
    expect(submitted.has("send_by_email")).toBe(false);
    // Not asked, so nothing is said about email.
    expect(screen.queryByText(fill(en.accounts.signInMail.notEmailed, { n: "1" }))).not.toBeInTheDocument();
  });

  test("ticks the option by default, sends it, and shows the password, the queued email and when it goes", async () => {
    resetPasswordAction.mockResolvedValue({ ok: true, oneTimePassword: "swordfish-1", emailed: true });
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
      ok: true,
      oneTimePassword: "swordfish-1",
      emailed: false,
      emailSkippedReason: "no_address",
    });
    renderCard(on);

    fireEvent.click(screen.getByRole("button", { name: en.accounts.card.resetPassword }));

    expect(await screen.findByText("swordfish-1")).toBeInTheDocument();
    expect(
      screen.getByText(
        fill(en.accounts.signInMail.group, { n: "1", reason: en.accounts.emailSkipped.no_address }),
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

describe("AccountCard, the sign-in services panel", () => {
  beforeEach(() => {
    unlinkIdentityAction.mockReset();
    unlinkIdentityAction.mockResolvedValue({ ok: true });
  });

  const microsoft: LinkedIdentity = {
    id: "6f1c2d3e-4b5a-4c6d-8e7f-901a2b3c4d5e",
    provider: "microsoft",
    issuer: "https://login.microsoftonline.com/tenant-1/v2.0",
    email: "s.popescu@example.edu",
    linkedAt: "2026-11-20T09:30:00Z",
    lastUsedAt: "2026-11-25T14:05:00Z",
  };
  const github: LinkedIdentity = {
    id: "7a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
    provider: "github",
    issuer: "https://github.com",
    email: "",
    linkedAt: "2026-11-21T08:00:00Z",
    lastUsedAt: "2026-11-21T08:00:00Z",
  };

  const renderCard = (identities: IdentityList | null, subject: Account = account(), viewer: string = viewerId) =>
    render(
      <AccountCard
        account={subject}
        roles={roles}
        identities={identities}
        viewerId={viewer}
        dict={en}
        statusChangedAtLabel={null}
        locale="en"
        mail={{ status: "off" }}
      />,
    );

  const panel = () => screen.getByRole("region", { name: en.accounts.card.identities });
  const row = (text: string) => within(panel()).getByText(text).closest("li") as HTMLElement;

  test("lists each service with its issuer, the email it gave, and when it was linked and last used", () => {
    renderCard({ hasPassword: true, items: [microsoft, github], unlinked: [] });

    const first = row("Microsoft");
    expect(first).toHaveTextContent(microsoft.issuer);
    expect(first).toHaveTextContent("s.popescu@example.edu");
    expect(first).toHaveTextContent(formatMoment(microsoft.linkedAt, { locale: "en" }));
    expect(first).toHaveTextContent(formatMoment(microsoft.lastUsedAt, { locale: "en" }));

    // The provider gave no email: the row says so rather than leaving a gap.
    expect(row("GitHub")).toHaveTextContent(en.accounts.card.identityNoEmail);
  });

  test("says the account signs in through no service when it has none", () => {
    renderCard({ hasPassword: true, items: [], unlinked: [] });

    expect(within(panel()).getByText(en.accounts.card.identitiesEmpty)).toBeInTheDocument();
    expect(within(panel()).queryByRole("button")).not.toBeInTheDocument();
  });

  test("asks first, naming the service and its email, says every session ends, and sends nothing on cancel", async () => {
    renderCard({ hasPassword: true, items: [microsoft, github], unlinked: [] });

    fireEvent.click(within(row("Microsoft")).getByRole("button", { name: en.accounts.card.unlinkIdentity }));

    const dialog = await screen.findByRole("dialog", {
      name: fill(en.accounts.card.unlinkIdentityConfirmTitle, {
        provider: "Microsoft",
        email: isolate("s.popescu@example.edu"),
      }),
    });
    expect(dialog).toHaveTextContent(fill(en.accounts.card.unlinkIdentityConfirmBody, { provider: "Microsoft" }));
    // The record holds that one external account, not every account at the service.
    expect(dialog).toHaveTextContent(
      /this Microsoft account cannot sign in to or link with this account again until an administrator allows it/,
    );
    expect(dialog).not.toHaveTextContent(en.accounts.card.unlinkIdentityLastWay);
    fireEvent.click(within(dialog).getAllByRole("button", { name: en.accounts.card.unlinkIdentityCancel })[0]);

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(unlinkIdentityAction).not.toHaveBeenCalled();
  });

  test("names the service alone when it gave no email", async () => {
    renderCard({ hasPassword: true, items: [github], unlinked: [] });

    fireEvent.click(within(row("GitHub")).getByRole("button", { name: en.accounts.card.unlinkIdentity }));

    expect(
      await screen.findByRole("dialog", {
        name: fill(en.accounts.card.unlinkIdentityConfirmTitleNoEmail, { provider: "GitHub" }),
      }),
    ).toBeInTheDocument();
  });

  test("unlinks that identity of this account once confirmed", () =>
    onFakeClock(async () => {
      renderCard({ hasPassword: true, items: [microsoft, github], unlinked: [] });

      fireEvent.click(within(row("GitHub")).getByRole("button", { name: en.accounts.card.unlinkIdentity }));
      await answerYesOnFakeClock(en.accounts.card.unlinkIdentityConfirm);

      expect(unlinkIdentityAction).toHaveBeenCalledTimes(1);
      const submitted = unlinkIdentityAction.mock.calls[0][1] as FormData;
      expect(submitted.get("userId")).toBe(account().id);
      expect(submitted.get("identityId")).toBe(github.id);
    }));

  test("warns that no other way in is linked, and that a password reset still lets the person in", async () => {
    renderCard({ hasPassword: false, items: [microsoft], unlinked: [] });

    fireEvent.click(within(row("Microsoft")).getByRole("button", { name: en.accounts.card.unlinkIdentity }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(fill(en.accounts.card.unlinkIdentityConfirmBody, { provider: "Microsoft" }));
    expect(dialog).toHaveTextContent(
      fill(en.accounts.card.unlinkIdentityLastWay, { action: en.accounts.card.resetPassword }),
    );
    expect(dialog).toHaveTextContent(/no password and no other sign-in service/);
    expect(dialog).toHaveTextContent(/The account has no password and no other sign-in service linked\. The person can still get back in through a password reset — their own by email, or “Reset password”\./);
  });

  test("gives no last-way warning while a password or another service remains", async () => {
    const { unmount } = renderCard({ hasPassword: true, items: [microsoft], unlinked: [] });
    fireEvent.click(within(row("Microsoft")).getByRole("button", { name: en.accounts.card.unlinkIdentity }));
    expect(await screen.findByRole("dialog")).not.toHaveTextContent(en.accounts.card.unlinkIdentityLastWay);
    unmount();

    renderCard({ hasPassword: false, items: [microsoft, github], unlinked: [] });
    fireEvent.click(within(row("Microsoft")).getByRole("button", { name: en.accounts.card.unlinkIdentity }));
    expect(await screen.findByRole("dialog")).not.toHaveTextContent(en.accounts.card.unlinkIdentityLastWay);
  });

  test("names an identity unlinked since the page loaded and offers a fresh copy", () =>
    onFakeClock(async () => {
      unlinkIdentityAction.mockResolvedValue({ code: "identity_not_found" });
      renderCard({ hasPassword: true, items: [microsoft], unlinked: [] });

      fireEvent.click(within(row("Microsoft")).getByRole("button", { name: en.accounts.card.unlinkIdentity }));
      await answerYesOnFakeClock(en.accounts.card.unlinkIdentityConfirm);

      expect(screen.getByRole("alert")).toHaveTextContent(en.errors.identity_not_found);
      expect(screen.getByRole("button", { name: en.accounts.card.identitiesReload })).toBeInTheDocument();
    }));

  const google: UnlinkedIdentity = {
    id: "3b0c1d2e-3f4a-4b5c-8d6e-7f8091a2b3c4",
    provider: "google",
    issuer: "https://accounts.google.com",
    email: "ana@example.edu",
    unlinkedAt: "2026-11-26T22:01:25Z",
  };

  const unlinkedSection = () => within(panel()).getByRole("list", { name: en.accounts.card.unlinkedHeading });

  test("lists the services unlinked from the account under their own heading, with when", () => {
    renderCard({ hasPassword: true, items: [microsoft], unlinked: [google] });

    const unlinked = within(unlinkedSection()).getByText("Google").closest("li") as HTMLElement;
    expect(unlinked).toHaveTextContent(google.issuer);
    expect(unlinked).toHaveTextContent("ana@example.edu");
    expect(unlinked).toHaveTextContent(formatMoment(google.unlinkedAt, { locale: "en" }));
    // The linked service is not among them.
    expect(within(unlinkedSection()).queryByText("Microsoft")).not.toBeInTheDocument();
  });

  test("lists unlinked services even when no service is linked", () => {
    renderCard({ hasPassword: true, items: [], unlinked: [google] });

    expect(within(panel()).getByText(en.accounts.card.identitiesEmpty)).toBeInTheDocument();
    expect(within(unlinkedSection()).getByText("Google")).toBeInTheDocument();
  });

  test("has no unlinked heading when nothing was unlinked", () => {
    renderCard({ hasPassword: true, items: [microsoft], unlinked: [] });

    expect(within(panel()).queryByRole("heading", { name: en.accounts.card.unlinkedHeading })).not.toBeInTheDocument();
  });

  test("allows an unlinked service again once confirmed, naming it", () =>
    onFakeClock(async () => {
      allowIdentityAction.mockReset();
      allowIdentityAction.mockResolvedValue({ ok: true });
      renderCard({ hasPassword: true, items: [], unlinked: [google] });

      fireEvent.click(within(unlinkedSection()).getByRole("button", { name: en.accounts.card.allowIdentity }));
      await flushFakeClock();
      expect(screen.getByRole("dialog")).toHaveTextContent(en.accounts.card.allowIdentityConfirmBody);
      await answerYesOnFakeClock(
        en.accounts.card.allowIdentityConfirm,
        fill(en.accounts.card.allowIdentityConfirmTitle, { provider: "Google", email: isolate("ana@example.edu") }),
      );

      expect(allowIdentityAction).toHaveBeenCalledTimes(1);
      const submitted = allowIdentityAction.mock.calls[0][1] as FormData;
      expect(submitted.get("userId")).toBe(account().id);
      expect(submitted.get("unlinkedId")).toBe(google.id);
    }));

  test("names a record lifted since the page loaded and offers a fresh copy", () =>
    onFakeClock(async () => {
      allowIdentityAction.mockReset();
      allowIdentityAction.mockResolvedValue({ code: "unlinked_identity_not_found" });
      renderCard({ hasPassword: true, items: [], unlinked: [{ ...google, email: "" }] });

      fireEvent.click(within(unlinkedSection()).getByRole("button", { name: en.accounts.card.allowIdentity }));
      await answerYesOnFakeClock(
        en.accounts.card.allowIdentityConfirm,
        fill(en.accounts.card.allowIdentityConfirmTitleNoEmail, { provider: "Google" }),
      );

      expect(screen.getByRole("alert")).toHaveTextContent(en.errors.unlinked_identity_not_found);
      expect(screen.getByRole("button", { name: en.accounts.card.identitiesReload })).toBeInTheDocument();
    }));

  test("is not shown on your own account, where the service refuses an unlink", () => {
    renderCard({ hasPassword: true, items: [microsoft], unlinked: [] }, account(), account().id);

    expect(screen.queryByRole("region", { name: en.accounts.card.identities })).not.toBeInTheDocument();
  });

  test("is not shown when the services could not be read, while the rest of the card is", () => {
    renderCard(null);

    expect(screen.queryByRole("region", { name: en.accounts.card.identities })).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: en.accounts.card.danger })).toBeInTheDocument();
  });

  test("is not shown on a deleted account", () => {
    renderCard({ hasPassword: true, items: [microsoft], unlinked: [] }, account({ status: "deleted" }));

    expect(screen.queryByRole("region", { name: en.accounts.card.identities })).not.toBeInTheDocument();
  });
});
