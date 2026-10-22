import { fireEvent, render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { MailTestState } from "./actions";

const sendTestMailAction = vi.hoisted(() => vi.fn(async (): Promise<MailTestState> => ({})));
vi.mock("./actions", () => ({ sendTestMailAction }));

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { MailTest } from "./mail-test";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  sendTestMailAction.mockReset();
});

const on = { enabled: true, from: "noreply@usm.md" };
const off = { enabled: false, from: "" };

function sendButton() {
  return screen.getByRole("button", { name: en.settings.mail.send });
}

describe("MailTest, the state of mail", () => {
  test("says mail is on and the address it is sent as", () => {
    render(<MailTest mail={on} dict={en} />);

    expect(screen.getByText(en.settings.mail.on.replace("{address}", "noreply@usm.md"))).toBeVisible();
    expect(sendButton()).toBeEnabled();
  });

  test("says mail is off, with no button to press", () => {
    render(<MailTest mail={off} dict={en} />);

    expect(screen.getByText(en.settings.mail.off)).toBeVisible();
    expect(screen.queryByRole("button", { name: en.settings.mail.send })).toBeNull();
  });
});

describe("MailTest, sending the test message", () => {
  test("says to which address the message went", async () => {
    sendTestMailAction.mockResolvedValueOnce({ sentTo: "root@usm.md" });
    render(<MailTest mail={on} dict={en} />);

    fireEvent.click(sendButton());

    expect(await screen.findByRole("status")).toHaveTextContent(
      en.settings.mail.sent.replace("{address}", "root@usm.md"),
    );
    expect(sendTestMailAction).toHaveBeenCalledTimes(1);
  });

  test("says why the relay refused, with its reply", async () => {
    sendTestMailAction.mockResolvedValueOnce({
      code: "mail_relay_failed",
      subject: "535 5.7.139 Authentication unsuccessful",
    });
    render(<MailTest mail={on} dict={en} />);

    fireEvent.click(sendButton());

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.errors.mail_relay_failed);
    expect(alert).toHaveTextContent("535 5.7.139 Authentication unsuccessful");
    expect(screen.queryByRole("status")).toBeNull();
  });

  test("says a refusal with no reply in its own sentence alone", async () => {
    sendTestMailAction.mockResolvedValueOnce({ code: "mail_no_address" });
    render(<MailTest mail={on} dict={en} />);

    fireEvent.click(sendButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.mail_no_address);
  });

  test.each(["mail_off", "mail_no_address", "mail_relay_failed", "mail_refused", "mail_test_too_often"])(
    "has its own sentence for %s",
    (code) => {
      expect((en.errors as Record<string, string>)[code]).toBeTruthy();
    },
  );
});
