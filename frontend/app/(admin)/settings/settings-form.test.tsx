import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

const saveSettingsAction = vi.hoisted(() => vi.fn(async () => ({ saved: true })));
vi.mock("./actions", () => ({ saveSettingsAction }));

import type { Settings } from "@/lib/api/settings";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { SettingsForm } from "./settings-form";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  saveSettingsAction.mockClear();
});

const settings = (over: Partial<Settings> = {}): Settings => ({
  name: "USM",
  contact: "",
  feedback: "",
  images: {},
  ...over,
});

function feedbackField() {
  return screen.getByLabelText(en.settings.feedback) as HTMLInputElement;
}

describe("SettingsForm, the feedback address", () => {
  test("shows the saved address", () => {
    render(<SettingsForm settings={settings({ feedback: "https://forms.gle/beta" })} dict={en} />);

    expect(feedbackField()).toHaveValue("https://forms.gle/beta");
  });

  test("refuses an address that is not https before sending anything", async () => {
    render(<SettingsForm settings={settings()} dict={en} />);

    fireEvent.change(feedbackField(), { target: { value: "http://forms.gle/beta" } });
    fireEvent.click(screen.getByRole("button", { name: en.settings.save }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.invalid_feedback_url);
    expect(feedbackField()).toHaveAttribute("aria-invalid", "true");
    expect(saveSettingsAction).not.toHaveBeenCalled();
  });

  test("sends an https address", async () => {
    render(<SettingsForm settings={settings()} dict={en} />);

    fireEvent.change(feedbackField(), { target: { value: "https://forms.gle/beta" } });
    fireEvent.click(screen.getByRole("button", { name: en.settings.save }));

    await waitFor(() => expect(saveSettingsAction).toHaveBeenCalledTimes(1));
    const sent = (saveSettingsAction.mock.calls[0] as unknown[])[1] as FormData;
    expect(sent.get("feedback")).toBe("https://forms.gle/beta");
    expect(screen.queryByText(en.errors.invalid_feedback_url)).not.toBeInTheDocument();
  });

  test("sends an empty address, which removes the link", async () => {
    render(<SettingsForm settings={settings({ feedback: "https://forms.gle/beta" })} dict={en} />);

    fireEvent.change(feedbackField(), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: en.settings.save }));

    await waitFor(() => expect(saveSettingsAction).toHaveBeenCalledTimes(1));
  });
});
