import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToString } from "react-dom/server";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("next/cache", () => ({ revalidatePath: vi.fn() }));
vi.mock("next/navigation", () => ({ redirect: vi.fn() }));

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

    expect(confirm).toHaveBeenCalledWith(t().confirm.replace("{title}", "Spring round"));
    expect(serverRequest).not.toHaveBeenCalled();
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

  test("the server's markup leaves the button disabled until the page is hydrated", () => {
    const html = renderToString(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);
    const page = new DOMParser().parseFromString(html, "text/html");

    expect(page.querySelector('button[type="submit"]')?.hasAttribute("disabled")).toBe(true);
  });

  test("hydrating the server's markup enables the button", async () => {
    const container = document.createElement("div");
    document.body.append(container);
    container.innerHTML = renderToString(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

    render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />, { container, hydrate: true });

    expect(await screen.findByRole("button", { name: t().action })).toBeEnabled();
    container.remove();
  });

  test("the button is the danger variant", () => {
    render(<DeleteDraftPanel contestId={contestId} title="Spring round" dict={dict} />);

    expect(screen.getByRole("button", { name: t().action }).className).toContain("bg-bad-wash");
  });
});
