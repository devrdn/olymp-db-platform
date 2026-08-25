import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { ContestSummary } from "@/lib/api/contests";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ContestRegister } from "./contest-register";

const nightInTheArchive: ContestSummary = {
  id: "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
  status: "running",
  enrollment: "open",
  questionMode: "multi",
  lang: "ru",
  title: "Ночь в архиве",
  description: "Опись пропала между полуночью и рассветом.",
  startsAt: "2026-11-08T19:00:00Z",
  endsAt: "2026-11-08T21:00:00Z",
};

let en: Dictionary;
let ro: Dictionary;

beforeAll(async () => {
  [en, ro] = await Promise.all([getDictionary("en"), getDictionary("ro")]);
});

describe("ContestRegister", () => {
  test("names a contest's state in the dictionary's language", () => {
    render(<ContestRegister contests={[nightInTheArchive]} total={1} dict={en} locale="en" />);

    const row = screen.getByRole("row", { name: /Ночь в архиве/ });

    expect(within(row).getByText("running")).toBeInTheDocument();
  });

  test("carries the same row into another language without touching the component", () => {
    render(<ContestRegister contests={[nightInTheArchive]} total={1} dict={ro} locale="ro" />);

    const row = screen.getByRole("row", { name: /Ночь în arhivă|Ночь в архиве/ });

    expect(within(row).getByText("în desfășurare")).toBeInTheDocument();
  });
});

describe("ContestRegister, nothing to show", () => {
  test("offers no filter reset when nothing has been created yet", () => {
    render(<ContestRegister contests={[]} total={0} dict={en} locale="en" />);

    expect(screen.getByText(en.contests.empty.title)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /clear/i })).not.toBeInTheDocument();
  });

  test("offers a filter reset when a filter is what emptied the register", () => {
    // Filters live in the URL, so clearing them is navigation, not a handler.
    render(
      <ContestRegister
        contests={[]}
        total={0}
        dict={en}
        locale="en"
        filtered
        resetHref="/en/contests"
      />,
    );

    expect(screen.getByText(en.contests.emptyFiltered.title)).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: en.contests.emptyFiltered.reset }),
    ).toHaveAttribute("href", "/en/contests");
  });
});

describe("ContestRegister, dates", () => {
  test("shows the contest window as readable dates, not as ISO strings", () => {
    render(<ContestRegister contests={[nightInTheArchive]} total={1} dict={en} locale="en" />);

    const row = screen.getByRole("row", { name: /Ночь в архиве/ });

    expect(row.textContent).not.toContain("2026-11-08T19:00:00Z");
    expect(within(row).getByText(/2026/)).toBeInTheDocument();
  });

  test("prints a one-day window as a date and a time range, not the date twice", () => {
    render(<ContestRegister contests={[nightInTheArchive]} total={1} dict={en} locale="en" />);

    const row = screen.getByRole("row", { name: /Ночь в архиве/ });

    // 19:00–21:00 UTC is one evening in Chisinau, so the date is said once.
    expect(within(row).getAllByText(/2026/)).toHaveLength(1);
    expect(within(row).getByText(/\d{2}:\d{2}.+\d{2}:\d{2}/)).toBeInTheDocument();
  });

  test("repeats the full moment when the window crosses midnight", () => {
    render(
      <ContestRegister
        contests={[{ ...nightInTheArchive, endsAt: "2026-11-09T03:00:00Z" }]}
        total={1}
        dict={en}
        locale="en"
      />,
    );

    const row = screen.getByRole("row", { name: /Ночь в архиве/ });

    expect(within(row).getAllByText(/2026/)).toHaveLength(2);
  });

  test("says so when a contest has no date rather than leaving the cell blank", () => {
    render(
      <ContestRegister
        contests={[{ ...nightInTheArchive, startsAt: undefined, endsAt: undefined }]}
        total={1}
        dict={en}
        locale="en"
      />,
    );

    const row = screen.getByRole("row", { name: /Ночь в архиве/ });

    expect(within(row).getByText(en.contests.unscheduled)).toBeInTheDocument();
  });
});
