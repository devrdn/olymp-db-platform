import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const forgotPasswordAction = vi.hoisted(() => vi.fn());
vi.mock("./actions", () => ({ forgotPasswordAction }));

import { ForgotForm } from "./forgot-form";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  forgotPasswordAction.mockReset();
});

function renderForm() {
  return render(<ForgotForm t={en.auth.forgot} errors={en.errors} />);
}

const submit = () => userEvent.click(screen.getByRole("button", { name: en.auth.forgot.submit }));

describe("ForgotForm", () => {
  test("asks for a login or an email, labelled from the dictionary", () => {
    renderForm();

    expect(screen.getByLabelText(en.auth.forgot.login)).toHaveAttribute("autocomplete", "username");
  });

  /** One answer for every account, and for none. */
  test("replaces the form with the one neutral confirmation", async () => {
    forgotPasswordAction.mockResolvedValue({ sent: true });
    renderForm();

    await submit();

    expect(await screen.findByRole("heading", { name: en.auth.forgot.sentTitle })).toBeInTheDocument();
    expect(screen.getByText(en.auth.forgot.sent)).toBeInTheDocument();
    expect(screen.queryByLabelText(en.auth.forgot.login)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.auth.forgot.backToSignIn })).toHaveAttribute("href", "/login");
  });

  test.each(["too_many_attempts", "invalid_request", "unreachable"] as const)(
    "says what %s means in the interface's own words",
    async (code) => {
      forgotPasswordAction.mockResolvedValue({ code, values: { login: "ana" } });
      renderForm();

      await submit();

      expect(await screen.findByRole("alert")).toHaveTextContent(en.errors[code]);
      expect(screen.getByLabelText(en.auth.forgot.login)).toHaveValue("ana");
    },
  );

  test("says recovery is not offered when the API has none", async () => {
    forgotPasswordAction.mockResolvedValue({ unavailable: true });
    renderForm();

    await submit();

    expect(await screen.findByText(en.auth.forgot.unavailable)).toBeInTheDocument();
  });
});

describe("ForgotForm, which field a refusal is about", () => {
  test("blames the field only when the request itself was malformed", async () => {
    forgotPasswordAction.mockResolvedValue({ code: "invalid_request", values: { login: "" } });
    render(<ForgotForm t={en.auth.forgot} errors={en.errors} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.forgot.submit }));
    await screen.findByRole("alert");

    expect(screen.getByLabelText(en.auth.forgot.login)).toHaveAttribute("aria-invalid", "true");
  });

  test("does not blame what was typed for the throttle", async () => {
    forgotPasswordAction.mockResolvedValue({ code: "too_many_attempts", values: { login: "ana" } });
    render(<ForgotForm t={en.auth.forgot} errors={en.errors} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.forgot.submit }));
    await screen.findByRole("alert");

    expect(screen.getByLabelText(en.auth.forgot.login)).not.toHaveAttribute("aria-invalid");
  });
});
