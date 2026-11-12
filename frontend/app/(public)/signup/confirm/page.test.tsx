import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

vi.mock("./actions", () => ({ confirmSignupAction: vi.fn(), resendCodeAction: vi.fn() }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import ConfirmPage from "./page";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

async function renderWith(searchParams: Record<string, string>) {
  render(
    await ConfirmPage({
      params: Promise.resolve({}),
      searchParams: Promise.resolve(searchParams),
    } as PageProps<"/signup/confirm">),
  );
}

describe("the confirmation page", () => {
  /** It names the inbox to look in, and never whether an account holds it. */
  test("names the address the code went to", async () => {
    await renderWith({ email: "ana@example.com" });

    expect(screen.getByText(en.auth.confirm.lede.replace("{email}", "ana@example.com"))).toBeInTheDocument();
    expect(screen.getByLabelText(en.auth.confirm.email)).toHaveValue("ana@example.com");
  });

  /** The address bar may select a sentence, never write one. */
  test("does not repeat text the address bar invented", async () => {
    await renderWith({ email: "Call 0800 123 now to claim your prize" });

    expect(screen.getByText(en.auth.confirm.ledeNoEmail)).toBeInTheDocument();
    expect(screen.queryByText(/0800/)).not.toBeInTheDocument();
  });

  test("still asks for the code when the address is missing", async () => {
    await renderWith({});

    expect(screen.getByText(en.auth.confirm.ledeNoEmail)).toBeInTheDocument();
    expect(screen.getByLabelText(en.auth.confirm.code)).toBeInTheDocument();
  });
});
