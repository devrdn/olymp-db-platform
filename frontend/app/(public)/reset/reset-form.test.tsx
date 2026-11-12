import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const resetPasswordAction = vi.hoisted(() => vi.fn());
vi.mock("./actions", () => ({ resetPasswordAction }));

import { ResetForm } from "./reset-form";

const TOKEN = "Q".repeat(43);

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  resetPasswordAction.mockReset();
});

function renderForm() {
  return render(
    <ResetForm t={en.auth.reset} passwordHint={en.auth.signUp.passwordHint} errors={en.errors} token={TOKEN} />,
  );
}

const submit = () => userEvent.click(screen.getByRole("button", { name: en.auth.reset.submit }));

describe("ResetForm", () => {
  test("asks for the new password twice, labelled from the dictionary", () => {
    renderForm();

    for (const label of [en.auth.reset.next, en.auth.reset.confirm]) {
      expect(screen.getByLabelText(label)).toHaveAttribute("autocomplete", "new-password");
    }
  });

  /** The person learns the rule before choosing, not from a refusal. */
  test("states the password rule under the new password", () => {
    renderForm();

    expect(screen.getByLabelText(en.auth.reset.next)).toHaveAccessibleDescription(en.auth.signUp.passwordHint);
  });

  test("submits the link's token without showing it", async () => {
    resetPasswordAction.mockResolvedValue({});
    const { container } = renderForm();

    expect(container).not.toHaveTextContent(TOKEN);
    await submit();

    const [, form] = resetPasswordAction.mock.calls.at(-1) as [unknown, FormData];
    expect(form.get("token")).toBe(TOKEN);
  });

  test.each(["reset_link_invalid", "weak_password", "password_mismatch", "too_many_attempts", "sign_in_busy"] as const)(
    "says what %s means in the interface's own words",
    async (code) => {
      resetPasswordAction.mockResolvedValue({ code });
      renderForm();

      await submit();

      expect(await screen.findByRole("alert")).toHaveTextContent(en.errors[code]);
    },
  );

  test("offers a new link when this one can no longer be used", async () => {
    resetPasswordAction.mockResolvedValue({ code: "reset_link_invalid" });
    renderForm();

    await submit();

    expect(await screen.findByRole("link", { name: en.auth.reset.requestNew })).toHaveAttribute("href", "/forgot");
  });

  test.each([
    ["weak_password", [true, false]],
    ["password_mismatch", [true, true]],
  ] as const)("blames the fields %s is about", async (code, [next, confirm]) => {
    resetPasswordAction.mockResolvedValue({ code });
    renderForm();

    await submit();
    await screen.findByRole("alert");

    const expectInvalid = (label: string, invalid: boolean) =>
      invalid
        ? expect(screen.getByLabelText(label)).toHaveAttribute("aria-invalid", "true")
        : expect(screen.getByLabelText(label)).not.toHaveAttribute("aria-invalid");
    expectInvalid(en.auth.reset.next, next);
    expectInvalid(en.auth.reset.confirm, confirm);
  });
});
