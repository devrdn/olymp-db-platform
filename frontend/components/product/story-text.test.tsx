import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { StoryText } from "./story-text";

/**
 * This component is a security boundary, not a formatter.
 *
 * The story is written by a contest manager — a less trusted role than an
 * administrator — and read by every participant during a contest. A `<script>`
 * that survived from the source into the page would be one manager taking
 * every participant's session.
 *
 * The defence is that nothing here ever builds an HTML string: the renderer
 * produces React elements, so there is no `dangerouslySetInnerHTML` anywhere
 * and no sanitiser to be got wrong. Raw HTML in the source is not parsed at
 * all — it is text, and text is what it stays.
 */
describe("StoryText, what it refuses to render", () => {
  test("does not execute a script somebody wrote into the story", () => {
    const { container } = render(<StoryText markdown={'<script>alert(1)</script>'} />);

    expect(container.querySelector("script")).toBeNull();
  });

  test("does not build an element out of raw HTML at all", () => {
    // Not sanitised afterwards — never parsed. An `<img onerror>` is the
    // classic way past a sanitiser, and there is nothing here to get past.
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
    // A story may cite a source. `noopener` because a tab opened from here can
    // otherwise reach back through `window.opener`.
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
    // A round-tripping editor is where a table quietly becomes HTML and stops
    // being editable as Markdown. Keeping the source as the source means the
    // author can write one and it survives.
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
 * The payloads somebody would actually try, against the real renderer.
 *
 * Not a substitute for the decision above — nothing here builds an HTML string,
 * so there is no filter to evade — but a filter relied on instead of that
 * decision would fail at least one of these.
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
 * The editor's own artefact, which every story written so far carries.
 *
 * Milkdown serialises an empty paragraph — and an empty table cell — as a
 * literal `<br />`. Raw HTML is deliberately not rendered here, so without
 * cleaning, a participant reads the four characters. The save now cleans
 * them, but the stories already in the database do not become right by
 * themselves, and rewriting somebody's text with a migration is not the way
 * to make them so.
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
