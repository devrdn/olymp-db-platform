import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { QuestionBody } from "./question-body";

describe("a question's wording", () => {
  test("renders the author's Markdown", () => {
    render(<QuestionBody markdown={"Who was in the **greenhouse**?"} />);

    expect(screen.getByText("greenhouse").tagName).toBe("STRONG");
  });

  // The same boundary as the story: StoryText builds elements, never HTML.
  test("leaves raw HTML as text rather than building it", () => {
    const { container } = render(<QuestionBody markdown={'<img src="x" onerror="alert(1)">\n\nWho?'} />);

    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByText("Who?")).toBeInTheDocument();
  });
});
