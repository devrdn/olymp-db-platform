import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import { fill, isolate } from "@/lib/i18n/fill";
import { answerYesOnFakeClock, onFakeClock } from "@/components/ui/confirm-dialog-test-support";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/server")>()),
  serverRequest,
}));
vi.mock("next/cache", () => ({ revalidatePath: vi.fn() }));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  redirect: vi.fn(),
}));

import { DeleteDraftPanel } from "./delete-draft-panel";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => serverRequest.mockReset());

const contestId = "f767af3b-f135-40d2-a3a6-82d368de1004";

describe("DeleteDraftPanel", () => {
  const t = () => dict.workspace.settings.deleteDraft;

  test("asks first, naming the draft, and sends nothing on cancel", async () => {
    const user = userEvent.setup();
    render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

    await user.click(screen.getByRole("button", { name: t().action }));

    expect(await screen.findByRole("dialog", { name: fill(t().confirm, { title: isolate("Spring round") }) })).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: dict.chrome.cancel })[0]);
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("names a title that reads like a replacement pattern literally", async () => {
    const user = userEvent.setup();
    render(<DeleteDraftPanel contestId={contestId} title="Q$&A 50$' off" dict={dict} />);

    await user.click(screen.getByRole("button", { name: t().action }));

    expect(await screen.findByRole("dialog")).toHaveTextContent("Q$&A 50$' off");
  });

  test("shows a refusal as a sentence", () =>
    onFakeClock(async () => {
      serverRequest.mockRejectedValueOnce(new ApiError("game_in_use", 409, "in use"));
      render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

      fireEvent.click(screen.getByRole("button", { name: t().action }));
      await answerYesOnFakeClock(t().action);

      expect(screen.getByRole("alert")).toHaveTextContent(dict.errors.game_in_use);
    }));

  test("the button stays disabled while the delete is in flight", () =>
    onFakeClock(async () => {
      serverRequest.mockReturnValueOnce(new Promise(() => {}));
      render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

      fireEvent.click(screen.getByRole("button", { name: t().action }));
      await answerYesOnFakeClock(t().action);

      expect(serverRequest).toHaveBeenCalledOnce();
      expect(screen.getByRole("button", { name: t().action })).toBeDisabled();
    }));

  test("the button is the danger variant", () => {
    render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

    expect(screen.getByRole("button", { name: t().action }).className).toContain("bg-bad-wash");
  });
});
