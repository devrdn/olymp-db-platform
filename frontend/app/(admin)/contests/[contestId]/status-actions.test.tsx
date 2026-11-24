import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { setStatusAction, refresh } = vi.hoisted(() => ({
  setStatusAction: vi.fn(async () => ({ code: "contest_changed" })),
  refresh: vi.fn(),
}));
vi.mock("./actions", () => ({ setStatusAction }));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

import { StatusActions } from "./status-actions";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

describe("a status change refused for a contest that moved since", () => {
  test("offers to reload, and the reload starts the form over", async () => {
    const user = userEvent.setup();
    render(<StatusActions contestId="c1" next={["published"]} dict={dict} />);

    await user.click(screen.getByRole("button", { name: dict.workspace.status.move.published }));
    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.contest_changed);
    await user.click(screen.getByRole("button", { name: dict.workspace.reload }));

    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
