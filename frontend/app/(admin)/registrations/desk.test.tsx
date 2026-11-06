import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { Registration } from "@/lib/api/registrations-terms";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in `next/headers`.
const { checkInAction } = vi.hoisted(() => ({ checkInAction: vi.fn() }));
vi.mock("./actions", () => ({ checkInAction }));

import { RegistrationDesk } from "./desk";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  checkInAction.mockReset();
});

const ana: Registration = {
  id: "11111111-1111-1111-1111-111111111111",
  firstName: "Ana",
  lastName: "Popescu",
  email: "ana@example.com",
  code: "K7M2QX",
  createdAt: "2026-11-06T15:04:05Z",
};

const ion: Registration = {
  id: "22222222-2222-2222-2222-222222222222",
  firstName: "Ion",
  lastName: "Rusu",
  email: "ion@example.org",
  code: "ABC234",
  createdAt: "2026-11-06T15:10:00Z",
  checkedInAt: "2026-11-06T18:00:00Z",
};

function renderDesk(registrations = [ana, ion]) {
  render(
    <RegistrationDesk registrations={registrations} t={en.registration.desk} errors={en.errors} locale="en" />,
  );
}

const t = () => en.registration.desk;
const search = () => screen.getByRole("searchbox", { name: t().search });
const toggleFor = (name: string) =>
  screen.getByRole("button", { name: t().hereFor.replace("{name}", name) });

function counter(label: string) {
  return screen.getByText(label, { selector: "dt" }).nextElementSibling;
}

describe("RegistrationDesk", () => {
  test("counts the registered and the arrived", () => {
    renderDesk();

    expect(counter(t().total)).toHaveTextContent("2");
    expect(counter(t().arrived)).toHaveTextContent("1");
  });

  test("shows each code split in two", () => {
    renderDesk();

    expect(screen.getByText("K7M-2QX")).toBeInTheDocument();
  });

  test.each([
    ["the code as printed", "K7M-2QX"],
    ["the code typed in lower case without the dash", "k7m2qx"],
    ["the code with spaces", "k7m 2qx"],
    ["the last name", "popescu"],
    ["the email", "ana@"],
  ])("finds a guest by %s", async (_, query) => {
    renderDesk();

    await userEvent.type(search(), query);

    expect(await screen.findByText("Ana Popescu")).toBeInTheDocument();
    expect(screen.queryByText("Ion Rusu")).not.toBeInTheDocument();
  });

  test("says when nobody matches, and clears the search", async () => {
    renderDesk();

    await userEvent.type(search(), "zzz");
    expect(await screen.findByText(t().noMatch)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: t().clear }));

    expect(search()).toHaveValue("");
    expect(screen.getByText("Ion Rusu")).toBeInTheDocument();
  });

  test("says when nobody has registered", () => {
    renderDesk([]);

    expect(screen.getByText(t().empty)).toBeInTheDocument();
  });

  test("marks a guest as arrived and counts them", async () => {
    checkInAction.mockResolvedValue({ registration: { ...ana, checkedInAt: "2026-11-06T18:30:00Z" } });
    renderDesk();

    expect(toggleFor("Ana Popescu")).toHaveAttribute("aria-pressed", "false");
    await userEvent.click(toggleFor("Ana Popescu"));

    expect(checkInAction).toHaveBeenCalledWith(ana.id, true);
    await vi.waitFor(() => expect(toggleFor("Ana Popescu")).toHaveAttribute("aria-pressed", "true"));
    expect(counter(t().arrived)).toHaveTextContent("2");
  });

  test("takes an arrival mark back", async () => {
    checkInAction.mockResolvedValue({ registration: { ...ion, checkedInAt: undefined } });
    renderDesk();

    expect(toggleFor("Ion Rusu")).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(toggleFor("Ion Rusu"));

    expect(checkInAction).toHaveBeenCalledWith(ion.id, false);
    await vi.waitFor(() => expect(toggleFor("Ion Rusu")).toHaveAttribute("aria-pressed", "false"));
  });

  test("announces a refused mark and leaves the row as it was", async () => {
    checkInAction.mockResolvedValue({ code: "registration_not_found" });
    renderDesk();

    await userEvent.click(toggleFor("Ana Popescu"));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.registration_not_found);
    expect(toggleFor("Ana Popescu")).toHaveAttribute("aria-pressed", "false");
  });

  test("keeps the details and the mark together in one row", () => {
    renderDesk();

    const row = screen.getByText("Ana Popescu").closest("tr")!;
    expect(within(row).getByText("ana@example.com")).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: t().hereFor.replace("{name}", "Ana Popescu") })).toBeInTheDocument();
  });
});
