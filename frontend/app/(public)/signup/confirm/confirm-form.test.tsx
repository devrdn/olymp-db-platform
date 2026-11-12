import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { confirmSignupAction, resendCodeAction } = vi.hoisted(() => ({
  confirmSignupAction: vi.fn(),
  resendCodeAction: vi.fn(),
}));
vi.mock("./actions", () => ({ confirmSignupAction, resendCodeAction }));

import { ConfirmForm } from "./confirm-form";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  confirmSignupAction.mockReset();
  resendCodeAction.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

function renderForm() {
  return render(<ConfirmForm t={en.auth.confirm} errors={en.errors} />);
}

const waitingFor = (seconds: number) => en.auth.confirm.resendIn.replace("{seconds}", String(seconds));

describe("ConfirmForm, the fields", () => {
  /** The address is the server's, from its cookie; nothing in the page can choose another. */
  test("asks only for the code, labelled from the dictionary", () => {
    const { container } = renderForm();

    expect(container.querySelector('[name="email"]')).toBeNull();
    const code = screen.getByLabelText(en.auth.confirm.code);
    expect(code).toHaveAttribute("autocomplete", "one-time-code");
    expect(code).toHaveAttribute("inputmode", "numeric");
  });
});

describe("ConfirmForm, a refused confirmation", () => {
  test.each([
    "signup_wrong_code",
    "signup_code_expired",
    "signup_not_available",
    "too_many_attempts",
    "invalid_request",
    "sign_in_busy",
    "account_blocked",
    "unreachable",
  ] as const)("says what %s means in the interface's own words", async (code) => {
    confirmSignupAction.mockResolvedValue({ code });
    renderForm();

    await userEvent.click(screen.getByRole("button", { name: en.auth.confirm.submit }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors[code]);
  });

  test("blames the code for a wrong code", async () => {
    confirmSignupAction.mockResolvedValue({ code: "signup_wrong_code" });
    renderForm();

    await userEvent.click(screen.getByRole("button", { name: en.auth.confirm.submit }));
    await screen.findByRole("alert");

    expect(screen.getByLabelText(en.auth.confirm.code)).toHaveAttribute("aria-invalid", "true");
  });

  /** An expired sign-up cannot be revived; the way on is to start again. */
  test("offers to sign up again when the code has expired", async () => {
    confirmSignupAction.mockResolvedValue({ code: "signup_code_expired" });
    renderForm();

    await userEvent.click(screen.getByRole("button", { name: en.auth.confirm.submit }));

    expect(await screen.findByRole("link", { name: en.auth.confirm.startOver })).toHaveAttribute("href", "/signup");
  });
});

describe("ConfirmForm, a new code", () => {
  test("waits sixty seconds after the first code before offering another", () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    renderForm();

    expect(screen.getByRole("button", { name: waitingFor(60) })).toBeDisabled();

    act(() => vi.advanceTimersByTime(59_000));
    expect(screen.getByRole("button", { name: waitingFor(1) })).toBeDisabled();

    act(() => vi.advanceTimersByTime(1_000));
    expect(screen.getByRole("button", { name: en.auth.confirm.resend })).toBeEnabled();
  });

  test("asks for a new code and counts down again", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    resendCodeAction.mockResolvedValue({ sent: true });
    renderForm();
    act(() => vi.advanceTimersByTime(60_000));

    await userEvent.click(screen.getByRole("button", { name: en.auth.confirm.resend }));

    expect(resendCodeAction).toHaveBeenCalledTimes(1);
    expect(await screen.findByRole("status")).toHaveTextContent(en.auth.confirm.resent);
    expect(screen.getByRole("button", { name: waitingFor(60) })).toBeDisabled();
  });

  test("waits as long as the API says when a code went out too recently", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    resendCodeAction.mockResolvedValue({ sent: false, code: "signup_too_soon", retryAfterSeconds: 37 });
    renderForm();
    act(() => vi.advanceTimersByTime(60_000));

    await userEvent.click(screen.getByRole("button", { name: en.auth.confirm.resend }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.signup_too_soon);
    expect(screen.getByRole("button", { name: waitingFor(37) })).toBeDisabled();
  });

  test("offers to sign up again when the sign-up a new code was asked for is gone", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    resendCodeAction.mockResolvedValue({ sent: false, code: "signup_code_expired" });
    renderForm();
    act(() => vi.advanceTimersByTime(60_000));

    await userEvent.click(screen.getByRole("button", { name: en.auth.confirm.resend }));

    expect(await screen.findByRole("link", { name: en.auth.confirm.startOver })).toHaveAttribute("href", "/signup");
  });

  test("says why when a new code was refused, and lets the person try again", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    resendCodeAction.mockResolvedValue({ sent: false, code: "too_many_attempts" });
    renderForm();
    act(() => vi.advanceTimersByTime(60_000));

    await userEvent.click(screen.getByRole("button", { name: en.auth.confirm.resend }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.too_many_attempts);
    expect(screen.getByRole("button", { name: en.auth.confirm.resend })).toBeEnabled();
  });
});
