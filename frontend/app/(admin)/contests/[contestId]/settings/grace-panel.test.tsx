import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import type { Contest } from "@/lib/api/contests";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("next/cache", () => ({ revalidatePath: vi.fn() }));

import { GracePanel } from "./grace-panel";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => serverRequest.mockReset());

function contest(gracePeriodMin: number): Contest {
  return {
    id: "f767af3b-f135-40d2-a3a6-82d368de1004",
    status: "finished",
    settings: { queryRateLimitPerMin: 0, gracePeriodMin, trustedDomainOnly: false },
  } as Contest;
}

describe("GracePanel", () => {
  const t = () => dict.workspace.settings.grace;

  test("states the grace the contest holds", () => {
    render(<GracePanel contest={contest(1440)} dict={dict} />);

    expect(screen.getByText(t().inForce.replace("{n}", "1440"))).toBeInTheDocument();
  });

  test("says the installation's default is in force when the contest has none", () => {
    render(<GracePanel contest={contest(0)} dict={dict} />);

    expect(screen.getByText(t().inForceDefault)).toBeInTheDocument();
  });

  test("bounds the input to what the server accepts", () => {
    render(<GracePanel contest={contest(0)} dict={dict} />);

    const field = screen.getByRole("spinbutton", { name: t().label });
    expect(field).toHaveAttribute("min", "1");
    expect(field).toHaveAttribute("max", "129600");
    expect(field).toHaveAccessibleDescription(t().hint);
  });

  test("shows the server's refusal to shorten as a sentence", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("grace_not_longer", 400, "not longer"));
    const user = userEvent.setup();
    render(<GracePanel contest={contest(0)} dict={dict} />);

    await user.type(screen.getByRole("spinbutton", { name: t().label }), "60");
    await user.click(screen.getByRole("button", { name: dict.workspace.settings.save }));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.grace_not_longer);
    expect(serverRequest).toHaveBeenCalledTimes(1);
  });

  test("refuses a value no longer than the contest's own grace before asking the server", async () => {
    const user = userEvent.setup();
    render(<GracePanel contest={contest(1440)} dict={dict} />);

    await user.type(screen.getByRole("spinbutton", { name: t().label }), "1440");
    await user.click(screen.getByRole("button", { name: dict.workspace.settings.save }));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.grace_not_longer);
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
