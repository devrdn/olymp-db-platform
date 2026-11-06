import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in `next/headers`.
const { registerAction, copyText } = vi.hoisted(() => ({
  registerAction: vi.fn(),
  copyText: vi.fn(),
}));
vi.mock("./actions", () => ({ registerAction }));
vi.mock("@/lib/clipboard", () => ({ copyText }));

import { RegisterForm } from "./register-form";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  registerAction.mockReset();
  copyText.mockReset();
});

const created = {
  status: "created",
  code: "K7M2QX",
  firstName: "Ana",
  lastName: "Popescu",
  email: "ana@example.com",
  account: "created",
  login: "ana@example.com",
  oneTimePassword: "Secret-123",
  emailSent: true,
};

function renderForm() {
  render(<RegisterForm t={en.registration} errors={en.errors} />);
}

async function submit() {
  await userEvent.type(screen.getByLabelText(en.registration.firstName), "Ana");
  await userEvent.type(screen.getByLabelText(en.registration.lastName), "Popescu");
  await userEvent.type(screen.getByLabelText(en.registration.email), "ana@example.com");
  await userEvent.click(screen.getByRole("button", { name: en.registration.submit }));
}

describe("RegisterForm, a new registration", () => {
  test("shows the code split in two, and where it was mailed", async () => {
    registerAction.mockResolvedValue({ outcome: created });
    renderForm();

    await submit();

    expect(await screen.findByRole("heading", { name: en.registration.created.title })).toBeInTheDocument();
    expect(screen.getByText("K7M-2QX")).toBeInTheDocument();
    expect(
      screen.getByText(en.registration.created.emailSent.replace("{email}", "ana@example.com")),
    ).toBeInTheDocument();
    expect(registerAction.mock.calls[0][1].get("email")).toBe("ana@example.com");
  });

  test("shows the new account's login and one-time password, and the way to sign in", async () => {
    registerAction.mockResolvedValue({ outcome: created });
    renderForm();

    await submit();

    expect(await screen.findByText("Secret-123")).toBeInTheDocument();
    expect(screen.getByText(en.registration.created.passwordNote)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.registration.signIn })).toHaveAttribute("href", "/login");
  });

  test("copies the code as printed", async () => {
    registerAction.mockResolvedValue({ outcome: created });
    copyText.mockResolvedValue(true);
    renderForm();

    await submit();
    await userEvent.click(await screen.findByRole("button", { name: en.registration.created.copyCode }));

    expect(copyText).toHaveBeenCalledWith("K7M-2QX");
    expect(await screen.findByText(en.registration.copied)).toBeInTheDocument();
  });

  test("copies the password", async () => {
    registerAction.mockResolvedValue({ outcome: created });
    copyText.mockResolvedValue(true);
    renderForm();

    await submit();
    await userEvent.click(await screen.findByRole("button", { name: en.registration.created.copyPassword }));

    expect(copyText).toHaveBeenCalledWith("Secret-123");
  });

  test("tells the visitor to keep the code when the email could not be sent", async () => {
    registerAction.mockResolvedValue({ outcome: { ...created, emailSent: false } });
    renderForm();

    await submit();

    expect(await screen.findByText(en.registration.created.emailNotSent)).toBeInTheDocument();
  });

  test("sends an existing account holder to sign in, with no password to show", async () => {
    registerAction.mockResolvedValue({
      outcome: { ...created, account: "existing", login: undefined, oneTimePassword: undefined },
    });
    renderForm();

    await submit();

    expect(await screen.findByText(en.registration.created.accountExisting)).toBeInTheDocument();
    expect(screen.queryByText(en.registration.created.password)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.registration.signIn })).toHaveAttribute("href", "/login");
    expect(screen.getByText("K7M-2QX")).toBeInTheDocument();
  });
});

describe("RegisterForm, a repeat registration", () => {
  test("says the code was mailed again and never shows it", async () => {
    registerAction.mockResolvedValue({
      outcome: { status: "existing", email: "ana@example.com", emailSent: true },
    });
    renderForm();

    await submit();

    expect(await screen.findByRole("heading", { name: en.registration.repeat.title })).toBeInTheDocument();
    expect(
      screen.getByText(en.registration.repeat.emailSent.replace("{email}", "ana@example.com")),
    ).toBeInTheDocument();
    expect(screen.queryByText(en.registration.created.codeLabel)).not.toBeInTheDocument();
  });

  test("points at the earlier email when nothing was sent this time", async () => {
    registerAction.mockResolvedValue({
      outcome: { status: "existing", email: "ana@example.com", emailSent: false },
    });
    renderForm();

    await submit();

    expect(
      await screen.findByText(en.registration.repeat.emailNotSent.replace("{email}", "ana@example.com")),
    ).toBeInTheDocument();
  });
});

describe("RegisterForm, a refusal", () => {
  test("announces the refusal in the interface's own words and keeps what was typed", async () => {
    registerAction.mockResolvedValue({
      code: "registration_full",
      values: { firstName: "Ana", lastName: "Popescu", email: "ana@example.com" },
    });
    renderForm();

    await submit();

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.registration_full);
    expect(screen.getByLabelText(en.registration.email)).toHaveValue("ana@example.com");
    expect(screen.getByLabelText(en.registration.firstName)).toHaveValue("Ana");
  });

  test("marks the email field when the email was refused", async () => {
    registerAction.mockResolvedValue({
      code: "registration_invalid_email",
      values: { firstName: "Ana", lastName: "Popescu", email: "ana@" },
    });
    renderForm();

    await submit();
    await screen.findByRole("alert");

    const email = screen.getByLabelText(en.registration.email);
    expect(email).toHaveAttribute("aria-invalid", "true");
    expect(email).toHaveAccessibleDescription(
      expect.stringContaining(en.errors.registration_invalid_email),
    );
    expect(screen.getByLabelText(en.registration.firstName)).not.toHaveAttribute("aria-invalid");
  });

  test("marks the name fields when a name was refused", async () => {
    registerAction.mockResolvedValue({
      code: "registration_invalid_name",
      values: { firstName: "", lastName: "Popescu", email: "ana@example.com" },
    });
    renderForm();

    await submit();
    await screen.findByRole("alert");

    expect(screen.getByLabelText(en.registration.firstName)).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText(en.registration.lastName)).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText(en.registration.email)).not.toHaveAttribute("aria-invalid");
  });
});
