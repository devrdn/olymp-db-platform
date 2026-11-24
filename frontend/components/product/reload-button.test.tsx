import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

const { refresh } = vi.hoisted(() => ({ refresh: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

import { ReloadButton } from "./reload-button";

beforeEach(() => refresh.mockReset());

describe("ReloadButton", () => {
  test("re-reads the page when pressed, and never submits the form around it", async () => {
    const submit = vi.fn((event: SubmitEvent) => event.preventDefault());
    const user = userEvent.setup();
    render(
      <form onSubmit={(event) => submit(event.nativeEvent as SubmitEvent)}>
        <ReloadButton label="Reload the page" />
      </form>,
    );

    await user.click(screen.getByRole("button", { name: "Reload the page" }));

    expect(refresh).toHaveBeenCalledTimes(1);
    expect(submit).not.toHaveBeenCalled();
  });

  test("lets the screen start over what the fresh copy replaces", async () => {
    const onReload = vi.fn();
    const user = userEvent.setup();
    render(<ReloadButton label="Reload the page" onReload={onReload} />);

    await user.click(screen.getByRole("button", { name: "Reload the page" }));

    expect(refresh).toHaveBeenCalledTimes(1);
    expect(onReload).toHaveBeenCalledTimes(1);
  });
});
