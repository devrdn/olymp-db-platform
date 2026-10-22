import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { EmailSummary, SendByEmailOption, summariseEmail } from "./email-delivery";

let en: Dictionary;
let ru: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
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
    const { container } = render(<EmailSummary rows={[{ login: "a", emailed: false }]} dict={en} />);

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
        dict={ru}
      />,
    );
    const t = ru.accounts.signInMail;

    expect(screen.getByText(t.queued.replace("{n}", "2"))).toBeVisible();
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
    render(<EmailSummary rows={[{ login: "ivanov", emailed: false, reason: "no_address" }]} dict={en} />);

    expect(screen.queryByText(/queued/)).not.toBeInTheDocument();
    expect(screen.getByText(en.accounts.signInMail.notEmailed.replace("{n}", "1"))).toBeVisible();
  });

  test("opens a short list and collapses a long one, logins included either way", () => {
    const many = Array.from({ length: 40 }, (_, i) => ({
      login: `s${i}`,
      emailed: false,
      reason: "no_address",
    }));

    const { unmount } = render(<EmailSummary rows={many.slice(0, 3)} dict={en} />);
    // jsdom does not hide a closed `<details>`, so check its `open` state.
    expect(screen.getByText("s0").closest("details")).toHaveAttribute("open");
    unmount();

    render(<EmailSummary rows={many} dict={en} />);
    expect(screen.getByText("s39").closest("details")).not.toHaveAttribute("open");
  });
});

describe("SendByEmailOption", () => {
  test("is a ticked box named send_by_email, with the hint as its description", () => {
    render(
      <form aria-label="f">
        <SendByEmailOption dict={en} />
      </form>,
    );

    const box = screen.getByRole("checkbox", { name: en.accounts.signInMail.option });
    expect(box).toBeChecked();
    expect(box).toHaveAttribute("name", "send_by_email");
    expect(box).toHaveAccessibleDescription(en.accounts.signInMail.optionHint);
    expect(new FormData(screen.getByRole<HTMLFormElement>("form")).get("send_by_email")).toBe("on");
  });
});
