import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action pulls in `next/headers`.
const enrollAction = vi.hoisted(() => vi.fn());
vi.mock("./actions", () => ({ enrollAction }));

import { EnrollButton } from "./enroll-button";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

const contestId = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";

async function join() {
  render(<EnrollButton contestId={contestId} dict={{ participant: en.participant, errors: en.errors }} />);
  await userEvent.click(screen.getByRole("button", { name: en.participant.join }));
  return screen.findByRole("alert");
}

describe("EnrollButton, refused for an address outside the site's domain", () => {
  test("says why, and names the domain the action found", async () => {
    enrollAction.mockResolvedValue({ code: "contest_trusted_email_required", domain: "example.edu" });

    const alert = await join();

    expect(alert).toHaveTextContent(en.errors.contest_trusted_email_required);
    expect(alert).toHaveTextContent(en.participant.trustedDomain.replace("{domain}", () => "example.edu"));
    expect(alert).toHaveTextContent("example.edu");
  });

  test("says why alone when no domain came with it", async () => {
    enrollAction.mockResolvedValue({ code: "contest_trusted_email_required" });

    const alert = await join();

    expect(alert).toHaveTextContent(en.errors.contest_trusted_email_required);
    // The template's own words, without the domain, so a blank or "undefined" fill fails too.
    const lead = en.participant.trustedDomain.split("{domain}")[0].trim();
    expect(lead).not.toBe("");
    expect(alert).not.toHaveTextContent(lead);
    expect(alert.querySelectorAll("p")).toHaveLength(1);
  });
});

describe("EnrollButton, refused where a reset link can confirm the address", () => {
  test("points to the forgotten-password page", async () => {
    enrollAction.mockResolvedValue({ code: "contest_trusted_email_required", domain: "example.edu", confirmByReset: true });

    const alert = await join();

    expect(screen.getByRole("link", { name: en.participant.trustedDomainReset })).toHaveAttribute("href", "/forgot");
    expect(alert).toHaveTextContent(en.participant.trustedDomainReset);
  });

  test("does not, where recovery is off", async () => {
    enrollAction.mockResolvedValue({ code: "contest_trusted_email_required", domain: "example.edu" });

    await join();

    expect(screen.queryByRole("link", { name: en.participant.trustedDomainReset })).not.toBeInTheDocument();
  });
});

describe("EnrollButton, any other refusal", () => {
  test("shows only the refusal's own sentence", async () => {
    enrollAction.mockResolvedValue({ code: "enrollment_closed", domain: "example.edu" });

    const alert = await join();

    expect(alert).toHaveTextContent(en.errors.enrollment_closed);
    expect(alert).not.toHaveTextContent("example.edu");
  });
});
