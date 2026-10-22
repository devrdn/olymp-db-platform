import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { EmailSummary, SendByEmailOption, sendingTime, summariseEmail } from "./email-delivery";

let en: Dictionary;
let ro: Dictionary;
let ru: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
  ro = await getDictionary("ro");
  ru = await getDictionary("ru");
});

describe("summariseEmail", () => {
  test("says nothing when email was not asked for", () => {
    expect(summariseEmail([{ login: "a", emailed: false }, { login: "b" }])).toBeNull();
  });

  test("counts what was queued and groups the rest by reason, known reasons first", () => {
    const summary = summariseEmail([
      { login: "petrov", emailed: true },
      { login: "x", emailed: false, reason: "invented_later" },
      { login: "ivanov", emailed: false, reason: "no_address" },
      { login: "blocked1", emailed: false, reason: "not_active" },
      { login: "sidorov", emailed: false, reason: "no_address" },
    ]);

    expect(summary).toEqual({
      queued: 1,
      notEmailed: 4,
      groups: [
        { reason: "no_address", logins: ["ivanov", "sidorov"] },
        { reason: "not_active", logins: ["blocked1"] },
        { reason: "invented_later", logins: ["x"] },
      ],
    });
  });
});

describe("EmailSummary", () => {
  test("renders nothing when email was not asked for", () => {
    const { container } = render(<EmailSummary rows={[{ login: "a", emailed: false }]} perMinute={25} dict={en} locale="en" />);

    expect(container).toBeEmptyDOMElement();
  });

  test("says how many were queued and names each account not emailed under its reason", () => {
    render(
      <EmailSummary
        rows={[
          { login: "petrov", emailed: true },
          { login: "kuznetsov", emailed: true },
          { login: "ivanov", emailed: false, reason: "no_address" },
          { login: "blocked1", emailed: false, reason: "not_active" },
          { login: "x", emailed: false, reason: "invented_later" },
        ]}
        perMinute={25}
        dict={ru}
        locale="ru"
      />,
    );
    const t = ru.accounts.signInMail;

    expect(screen.getByText("2 письма с данными для входа поставлены в очередь.")).toBeVisible();
    expect(screen.getByText(/Отправка займёт около 1 минуты/)).toBeVisible();
    expect(screen.getByText(t.notEmailed.replace("{n}", "3"))).toBeVisible();

    const noAddress = screen
      .getByText(t.group.replace("{n}", "1").replace("{reason}", ru.accounts.emailSkipped.no_address))
      .closest("details") as HTMLElement;
    expect(within(noAddress).getByText("ivanov")).toBeInTheDocument();

    const notActive = screen
      .getByText(t.group.replace("{n}", "1").replace("{reason}", ru.accounts.emailSkipped.not_active))
      .closest("details") as HTMLElement;
    expect(within(notActive).getByText("blocked1")).toBeInTheDocument();

    // A reason newer than this build is shown raw rather than dropped.
    expect(screen.getByText(t.group.replace("{n}", "1").replace("{reason}", "invented_later"))).toBeVisible();
  });

  test("leaves out the queued line when nothing was queued", () => {
    render(<EmailSummary rows={[{ login: "ivanov", emailed: false, reason: "no_address" }]} perMinute={25} dict={en} locale="en" />);

    expect(screen.queryByText(/queued/)).not.toBeInTheDocument();
    expect(screen.queryByText(en.accounts.signInMail.replaced)).not.toBeInTheDocument();
    expect(screen.getByText(en.accounts.signInMail.notEmailed.replace("{n}", "1"))).toBeVisible();
  });

  test("opens a short list and collapses a long one, logins included either way", () => {
    const many = Array.from({ length: 40 }, (_, i) => ({
      login: `s${i}`,
      emailed: false,
      reason: "no_address",
    }));

    const { unmount } = render(<EmailSummary rows={many.slice(0, 3)} perMinute={25} dict={en} locale="en" />);
    // jsdom does not hide a closed `<details>`, so check its `open` state.
    expect(screen.getByText("s0").closest("details")).toHaveAttribute("open");
    unmount();

    render(<EmailSummary rows={many} perMinute={25} dict={en} locale="en" />);
    expect(screen.getByText("s39").closest("details")).not.toHaveAttribute("open");
  });
});

describe("sendingTime", () => {
  test("says how long the messages take at the outbox's rate, rounded up, in each language", () => {
    expect(sendingTime(1, 25, en, "en")).toBe(
      "It should go out within about 1 minute, later if other email is already waiting.",
    );
    expect(sendingTime(500, 25, en, "en")).toBe(
      "Sending them takes about 20 minutes, longer if other email is already waiting.",
    );
    expect(sendingTime(26, 25, en, "en")).toMatch(/about 2 minutes/);
    expect(sendingTime(500, 25, ro, "ro")).toMatch(/aproximativ 20 de minute/);
    expect(sendingTime(40, 25, ro, "ro")).toMatch(/aproximativ 2 minute,/);
    expect(sendingTime(500, 25, ru, "ru")).toMatch(/около 20 минут,/);
    expect(sendingTime(525, 25, ru, "ru")).toMatch(/около 21 минуты,/);
    expect(sendingTime(1, 25, ru, "ru")).toMatch(/^Оно уйдёт в течение 1 минуты,/);
  });

  test("leaves the time out when the rate is unknown, and still says a later reset replaces the email", () => {
    render(<EmailSummary rows={[{ login: "a", emailed: true }]} perMinute={0} dict={en} locale="en" />);

    expect(screen.getByText("1 sign-in email queued.")).toBeVisible();
    expect(screen.queryByText(/minute/)).not.toBeInTheDocument();
    expect(screen.getByText(en.accounts.signInMail.replaced)).toBeVisible();
  });
});

describe("SendByEmailOption", () => {
  test("is a ticked box named send_by_email while mail is on, with the hint as its description", () => {
    render(
      <form aria-label="f">
        <SendByEmailOption mail={{ status: "on", perMinute: 25 }} dict={en} />
      </form>,
    );

    const box = screen.getByRole("checkbox", { name: en.accounts.signInMail.option });
    expect(box).toBeChecked();
    expect(box).toHaveAttribute("name", "send_by_email");
    expect(box).toHaveAccessibleDescription(en.accounts.signInMail.optionHint);
    expect(new FormData(screen.getByRole<HTMLFormElement>("form")).get("send_by_email")).toBe("on");
  });

  test("renders nothing while mail is off", () => {
    const { container } = render(<SendByEmailOption mail={{ status: "off" }} dict={en} />);

    expect(container).toBeEmptyDOMElement();
  });

  test("says it could not check, instead of the option, when the status could not be read", () => {
    render(<SendByEmailOption mail={{ status: "unknown" }} dict={ro} />);

    expect(screen.getByText(ro.accounts.signInMail.unknown)).toBeVisible();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });
});
