import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { SwitchState } from "./actions";

const switchProviderAction = vi.hoisted(() => vi.fn(async (_previous: SwitchState, _form: FormData): Promise<SwitchState> => ({})));
vi.mock("./actions", () => ({ switchProviderAction }));

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ProviderSwitch } from "./provider-switch";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  switchProviderAction.mockReset();
  switchProviderAction.mockResolvedValue({});
});

describe("ProviderSwitch", () => {
  test("an enabled provider offers to switch off and submits the opposite", async () => {
    render(<ProviderSwitch provider="microsoft" enabled describedBy="integration-microsoft" dict={en} />);

    fireEvent.click(screen.getByRole("button", { name: en.settings.integrations.switchOff }));

    await waitFor(() => expect(switchProviderAction).toHaveBeenCalled());
    const data = switchProviderAction.mock.calls[0][1];
    expect(data.get("provider")).toBe("microsoft");
    expect(data.get("enabled")).toBe("false");
  });

  test("a disabled provider offers to switch on and submits the opposite", async () => {
    render(<ProviderSwitch provider="google" enabled={false} describedBy="integration-google" dict={en} />);

    fireEvent.click(screen.getByRole("button", { name: en.settings.integrations.switchOn }));

    await waitFor(() => expect(switchProviderAction).toHaveBeenCalled());
    const data = switchProviderAction.mock.calls[0][1];
    expect(data.get("provider")).toBe("google");
    expect(data.get("enabled")).toBe("true");
  });

  test("the button is described by its provider's heading, so a list of buttons tells them apart", () => {
    render(
      <>
        <h2 id="integration-github">GitHub</h2>
        <ProviderSwitch provider="github" enabled describedBy="integration-github" dict={en} />
      </>,
    );

    expect(screen.getByRole("button", { name: en.settings.integrations.switchOff })).toHaveAccessibleDescription("GitHub");
  });

  test("says why it failed, in the dictionary's words", async () => {
    switchProviderAction.mockResolvedValueOnce({ code: "not_found" });
    render(<ProviderSwitch provider="google" enabled describedBy="integration-google" dict={en} />);

    fireEvent.click(screen.getByRole("button", { name: en.settings.integrations.switchOff }));

    expect(await screen.findByText(en.errors.not_found)).toBeVisible();
  });
});
