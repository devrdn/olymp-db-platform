import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { SwitchState } from "./actions";

const switchArchiveAction = vi.hoisted(() => vi.fn<(previous: SwitchState, form: FormData) => Promise<SwitchState>>(async () => ({})));
vi.mock("./actions", () => ({ switchArchiveAction }));

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ArchiveSwitch } from "./archive-switch";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  switchArchiveAction.mockReset();
  switchArchiveAction.mockResolvedValue({});
});

describe("ArchiveSwitch", () => {
  test("an archive that is off offers to switch on and submits the opposite", async () => {
    render(<ArchiveSwitch enabled={false} dict={en} />);

    fireEvent.click(screen.getByRole("button", { name: en.settings.archive.switchOn }));

    await waitFor(() => expect(switchArchiveAction).toHaveBeenCalled());
    expect(switchArchiveAction.mock.calls[0][1].get("enabled")).toBe("true");
  });

  test("an archive that is on offers to switch off and submits the opposite", async () => {
    render(<ArchiveSwitch enabled dict={en} />);

    fireEvent.click(screen.getByRole("button", { name: en.settings.archive.switchOff }));

    await waitFor(() => expect(switchArchiveAction).toHaveBeenCalled());
    expect(switchArchiveAction.mock.calls[0][1].get("enabled")).toBe("false");
  });

  test("says where the archive stands", () => {
    render(<ArchiveSwitch enabled dict={en} />);

    expect(screen.getByText(en.settings.archive.on)).toBeVisible();
  });

  test("says why it failed, in the dictionary's words", async () => {
    switchArchiveAction.mockResolvedValueOnce({ code: "forbidden" });
    render(<ArchiveSwitch enabled dict={en} />);

    fireEvent.click(screen.getByRole("button", { name: en.settings.archive.switchOff }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.forbidden);
  });
});
