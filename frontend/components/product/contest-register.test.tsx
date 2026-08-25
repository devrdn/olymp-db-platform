import { render, screen, within } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import type { ContestSummary } from "@/lib/api/contests";

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

describe("ContestRegister", () => {
  test("gives every contest a row naming its state in words", () => {
    render(<ContestRegister contests={[nightInTheArchive]} total={1} />);

    const row = screen.getByRole("row", { name: /Ночь в архиве/ });

    expect(within(row).getByText("идёт")).toBeInTheDocument();
  });
});

describe("ContestRegister, nothing to show", () => {
  test("offers no filter reset when nothing has been created yet", () => {
    render(<ContestRegister contests={[]} total={0} />);

    expect(screen.getByText("Олимпиад пока нет")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /сбросить/i })).not.toBeInTheDocument();
  });

  test("offers a filter reset when a filter is what emptied the register", () => {
    // Filters live in the URL, so clearing them is navigation, not a handler.
    render(<ContestRegister contests={[]} total={0} filtered resetHref="/admin/contests" />);

    expect(screen.getByText("Ничего не нашлось")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /сбросить фильтры/i })).toHaveAttribute(
      "href",
      "/admin/contests",
    );
  });
});

describe("ContestRegister, dates", () => {
  test("shows the start as a readable date, not as an ISO string", () => {
    render(<ContestRegister contests={[nightInTheArchive]} total={1} />);

    const row = screen.getByRole("row", { name: /Ночь в архиве/ });

    expect(within(row).getByText("8 нояб. 2026 г., 21:00")).toBeInTheDocument();
    expect(within(row).queryByText(/2026-11-08T19:00:00Z/)).not.toBeInTheDocument();
  });
});
