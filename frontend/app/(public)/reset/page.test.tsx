import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

vi.mock("./actions", () => ({ resetPasswordAction: vi.fn() }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import ResetPage, { generateMetadata } from "./page";

const TOKEN = "Q".repeat(43);

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

async function renderWith(searchParams: Record<string, string>) {
  return render(
    await ResetPage({ params: Promise.resolve({}), searchParams: Promise.resolve(searchParams) } as PageProps<"/reset">),
  );
}

describe("the reset page", () => {
  test("asks for the new password when the link carries a token, without showing it", async () => {
    const { container } = await renderWith({ token: TOKEN });

    expect(screen.getByLabelText(en.auth.reset.next)).toBeInTheDocument();
    expect(container).not.toHaveTextContent(TOKEN);
  });

  test("says the link cannot be used, and offers a new one, when it carries no token", async () => {
    await renderWith({});

    expect(screen.getByText(en.errors.reset_link_invalid)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.auth.reset.requestNew })).toHaveAttribute("href", "/forgot");
    expect(screen.queryByLabelText(en.auth.reset.next)).not.toBeInTheDocument();
  });

  test("says the same for a token no link could carry", async () => {
    await renderWith({ token: "../auth/me" });

    expect(screen.getByText(en.errors.reset_link_invalid)).toBeInTheDocument();
  });

  test("tells the browser to send no referrer and search engines to stay away", async () => {
    expect(await generateMetadata()).toMatchObject({
      referrer: "no-referrer",
      robots: { index: false, follow: false },
    });
  });
});
