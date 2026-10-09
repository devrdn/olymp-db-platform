import { readFileSync } from "node:fs";
import path from "node:path";

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import { Button } from "./button";

describe("the button", () => {
  test("is a native button, so the browser's own semantics apply", () => {
    render(<Button>Run</Button>);

    const button = screen.getByRole("button", { name: "Run" });
    expect(button.tagName).toBe("BUTTON");
    // No `type`: inside a form it still submits.
    expect(button).not.toHaveAttribute("type");
  });

  test("takes a type when one is asked for", () => {
    render(<Button type="button">Download</Button>);

    expect(screen.getByRole("button", { name: "Download" })).toHaveAttribute("type", "button");
  });

  test("a disabled button is disabled to the browser, and does not fire", async () => {
    const clicked = vi.fn();
    render(
      <Button disabled onClick={clicked}>
        Submit
      </Button>,
    );

    const button = screen.getByRole("button", { name: "Submit" });
    expect(button).toBeDisabled();
    await userEvent.click(button);
    expect(clicked).not.toHaveBeenCalled();
  });

  test("carries the variant and size classes it was asked for", () => {
    render(
      <Button variant="danger" size="sm">
        Delete
      </Button>,
    );

    const button = screen.getByRole("button", { name: "Delete" });
    expect(button.className).toContain("bg-bad-wash");
    expect(button.className).toContain("h-7");
  });
});

/**
 * Base UI once cost the play route about 25 KiB gzipped for a `<button>` and an
 * `<input>`. The rendered output cannot show it is gone, so the source is
 * asserted.
 */
describe("what the two plainest controls import", () => {
  test.each(["button.tsx", "input.tsx"])("%s reaches no component library", (file) => {
    const source = readFileSync(path.resolve(__dirname, file), "utf8");
    const imports = [...source.matchAll(/from\s+["']([^"']+)["']/g)].map(([, specifier]) => specifier);

    expect(imports).not.toHaveLength(0);
    expect(imports.filter((specifier) => specifier.startsWith("@base-ui"))).toEqual([]);
  });
});
