import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";

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

  test("asks first, naming the draft, and sends nothing on no", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const user = userEvent.setup();
    render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

    await user.click(screen.getByRole("button", { name: t().action }));

    expect(confirm).toHaveBeenCalledWith(fill(t().confirm, { title: "Spring round" }));
    expect(serverRequest).not.toHaveBeenCalled();
    confirm.mockRestore();
  });

  test("names a title that reads like a replacement pattern literally", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const user = userEvent.setup();
    render(<DeleteDraftPanel contestId={contestId} title="Q$&A 50$' off" dict={dict} />);

    await user.click(screen.getByRole("button", { name: t().action }));

    expect(confirm).toHaveBeenCalledWith(expect.stringContaining("Q$&A 50$' off"));
    confirm.mockRestore();
  });

  test("shows a refusal as a sentence", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    serverRequest.mockRejectedValueOnce(new ApiError("game_in_use", 409, "in use"));
    const user = userEvent.setup();
    render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

    await user.click(screen.getByRole("button", { name: t().action }));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.game_in_use);
    confirm.mockRestore();
  });

  test("the button stays disabled while the delete is in flight", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    serverRequest.mockReturnValueOnce(new Promise(() => {}));
    const user = userEvent.setup();
    render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

    await user.click(screen.getByRole("button", { name: t().action }));

    expect(serverRequest).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: t().action })).toBeDisabled();
    confirm.mockRestore();
  });

  test("the button is the danger variant", () => {
    render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

    expect(screen.getByRole("button", { name: t().action }).className).toContain("bg-bad-wash");
  });
});
