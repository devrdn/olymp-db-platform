import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { StoryText } from "./story-text";

/**
 * A security boundary: a manager writes the story and every participant reads
 * it, so a surviving `<script>` would take every participant's session. Nothing
 * builds an HTML string; raw HTML stays text.
 */
describe("StoryText, what it refuses to render", () => {
  test("does not execute a script somebody wrote into the story", () => {
    const { container } = render(<StoryText markdown={'<script>alert(1)</script>'} />);

    expect(container.querySelector("script")).toBeNull();
  });

  test("does not build an element out of raw HTML at all", () => {
    // Never parsed, so an `<img onerror>` has no sanitiser to get past.
    const { container } = render(
      <StoryText markdown={'<img src="x" onerror="alert(1)">' + "\n\nA body in the stacks."} />,
    );

    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByText("A body in the stacks.")).toBeInTheDocument();
  });

  test("refuses a link that would run code instead of going somewhere", () => {
    const { container } = render(<StoryText markdown={"[press me](javascript:alert(1))"} />);

    const link = container.querySelector("a");
    expect(link?.getAttribute("href") ?? "").not.toMatch(/^javascript:/i);
  });

  test("sends a link that does go somewhere out of the page safely", () => {
    // `noopener`, so the opened tab cannot reach back through `window.opener`.
    render(<StoryText markdown={"[the archive](https://example.edu/archive)"} />);

    const link = screen.getByRole("link", { name: "the archive" });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link.getAttribute("rel")).toContain("noopener");
  });
});

describe("StoryText, what it does render", () => {
  test("renders the shapes a story is written in", () => {
    render(
      <StoryText
        markdown={"# The Greenhouse\n\nThe gardener was **dead**.\n\n- a lamp\n- a key"}
      />,
    );

    expect(screen.getByRole("heading", { name: "The Greenhouse" })).toBeInTheDocument();
    expect(screen.getByText("dead")).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
  });

  test("renders a table, which is why this is not a WYSIWYG", () => {
    // A table stays Markdown in the source and renders as a table.
    render(
      <StoryText markdown={"| Suspect | Alibi |\n| --- | --- |\n| Butler | none |"} />,
    );

    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "Butler" })).toBeInTheDocument();
  });

  test("says nothing at all when there is nothing written yet", () => {
    const { container } = render(<StoryText markdown="   " />);

    expect(container.textContent?.trim()).toBe("");
  });
});

/**
 * Common payloads against the real renderer; a filter relied on instead of
 * never building HTML would fail at least one.
 */
describe("StoryText against the usual payloads", () => {
  const payloads = [
    '<script>alert(1)</script>',
    '<img src=x onerror=alert(1)>',
    '<svg/onload=alert(1)>',
    '<iframe src="javascript:alert(1)"></iframe>',
    '<a href="javascript:alert(1)">x</a>',
    '[x](javascript:alert(1))',
    '[x](JaVaScRiPt:alert(1))',
    '[x](data:text/html,<script>alert(1)</script>)',
    '<div onclick="alert(1)">click</div>',
    '<style>body{display:none}</style>',
    '<object data="javascript:alert(1)">',
    '![x](javascript:alert(1))',
  ];

  test.each(payloads)("renders no executable element or url for %s", (payload) => {
    const { container } = render(<StoryText markdown={payload} />);

    expect(container.querySelector("script, iframe, object, embed, style, svg")).toBeNull();
    for (const el of container.querySelectorAll("*")) {
      for (const attr of el.attributes) {
        expect(attr.name.toLowerCase()).not.toMatch(/^on/);
        if (attr.name === "href" || attr.name === "src") {
          expect(attr.value.toLowerCase()).not.toMatch(/^(javascript|data):/);
        }
      }
    }
  });
});

/**
 * Milkdown serialises an empty paragraph or table cell as a literal `<br />`.
 * Stored stories still carry it, and raw HTML is not rendered, so the renderer
 * cleans it rather than a migration rewriting authors' text.
 */
describe("the markdown a WYSIWYG editor produced", () => {
  test("does not print the editor's empty-paragraph breaks at the reader", () => {
    render(<StoryText markdown={"# A title\n\n<br />\n\nA body in the stacks."} />);

    expect(screen.queryByText(/<br/)).not.toBeInTheDocument();
    expect(screen.getByText("A body in the stacks.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "A title" })).toBeInTheDocument();
  });

  test("still shows a break that lives inside a fenced block, which is content", () => {
    render(<StoryText markdown={"```html\n<br />\n```"} />);

    expect(screen.getByText(/<br \/>/)).toBeInTheDocument();
  });
});
