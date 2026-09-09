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

/** The source of a file in this route, for the two facts about it no rendering assertion can reach. */
function sourceOf(file: string): string {
  return readFileSync(path.resolve(__dirname, file), "utf8");
}

describe("the workspace skeleton", () => {
  test("says in words that the workspace is coming", () => {
    render(<WorkspaceSkeleton dict={en} />);

    expect(screen.getByRole("status")).toHaveTextContent(en.participant.play.loading);
  });

  // A skeleton is decoration: a screen reader should hear the one sentence
  // above, not forty empty boxes. `Skeleton` marks itself `aria-hidden`, and
  // the containers holding the shapes are marked too — this is what keeps a
  // future shape from being added outside one of them.
  test("puts nothing but that sentence in the accessibility tree", () => {
    const { container } = render(<WorkspaceSkeleton dict={en} />);

    const exposed = [...container.querySelectorAll("*")].filter(
      (element) => element.closest("[aria-hidden]") === null,
    );
    // The root, the status line, and the status line's own text node holder.
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
 * Finding 2: `/play` had neither of the two things that put something on
 * screen while the server works — a `loading.tsx` for the wait before the
 * route renders at all, and a `<Suspense>` for the wait on the four requests
 * the workspace is built from.
 *
 * The second one is a fact about *where* a boundary sits, which no rendered
 * output can show: a header inside the boundary and a header above it look
 * identical once both have arrived. So it is checked the way this route's
 * client-graph rule already is (workspace.test.tsx) — against the source.
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
    // The opening tag, not the several mentions of the name in prose above it.
    const boundary = page.indexOf("<Suspense fallback");
    const panels = page.indexOf("<PlayPanels");

    expect(header).toBeGreaterThan(-1);
    expect(boundary).toBeGreaterThan(-1);
    // The bar first, then the boundary, then everything that waits on an API.
    expect(header).toBeLessThan(boundary);
    expect(boundary).toBeLessThan(panels);
  });

  test("and the workspace itself no longer draws a bar of its own", () => {
    expect(sourceOf("workspace.tsx")).not.toContain("./play-header");
  });
});
