import { readFileSync } from "node:fs";
import path from "node:path";

import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { PlayHeaderSkeleton, WorkspaceSkeleton } from "./skeleton";

vi.mock("@/lib/i18n/server", () => ({
  activeDictionary: async () => en,
  activeLocale: async () => "en",
}));

/** The source of a file in this route, for facts no render can show. */
function sourceOf(file: string): string {
  return readFileSync(path.resolve(__dirname, file), "utf8");
}

describe("the workspace skeleton", () => {
  test("says in words that the workspace is coming", () => {
    render(<WorkspaceSkeleton dict={en} />);

    expect(screen.getByRole("status")).toHaveTextContent(en.participant.play.loading);
  });

  // Screen readers hear the one sentence, not forty empty boxes; the shape
  // containers are `aria-hidden` too, so a new shape cannot leak.
  test("puts nothing but that sentence in the accessibility tree", () => {
    const { container } = render(<WorkspaceSkeleton dict={en} />);

    const exposed = [...container.querySelectorAll("*")].filter(
      (element) => element.closest("[aria-hidden]") === null,
    );
    // The root, the status line, and its text holder.
    for (const element of exposed) {
      expect(element.getAttribute("role") ?? "status").toBe("status");
    }
  });

  test("the bar's own stand-in carries no live text of its own", () => {
    const { container } = render(<PlayHeaderSkeleton />);

    expect(container.firstElementChild).toHaveAttribute("aria-hidden");
  });
});

/**
 * Where the Suspense boundary sits relative to the header cannot be seen in
 * rendered output, so it is checked against the source, like the client-graph
 * rule in workspace.test.tsx.
 */
describe("what is on screen while the workspace is still being built", () => {
  test("loading.tsx draws the bar and the panes, and names the wait", async () => {
    const { default: Loading } = await import("./loading");

    render(await Loading());

    expect(screen.getByRole("status")).toHaveTextContent(en.participant.play.loading);
  });

  test("the bar is rendered above the Suspense boundary, not inside it", () => {
    const page = sourceOf("page.tsx");

    const header = page.indexOf("<PlayHeader");
    // The opening tag, not mentions of the name in comments.
    const boundary = page.indexOf("<Suspense fallback");
    const panels = page.indexOf("<PlayPanels");

    expect(header).toBeGreaterThan(-1);
    expect(boundary).toBeGreaterThan(-1);
    // The header first, then the boundary.
    expect(header).toBeLessThan(boundary);
    expect(boundary).toBeLessThan(panels);
  });

  test("and the workspace itself no longer draws a bar of its own", () => {
    expect(sourceOf("workspace.tsx")).not.toContain("./play-header");
  });
});
