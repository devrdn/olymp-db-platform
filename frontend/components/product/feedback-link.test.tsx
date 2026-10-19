import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { FeedbackLink } from "./feedback-link";

const labels = en.participant.feedback;

describe("FeedbackLink", () => {
  test("opens the address in a new tab that cannot reach back into this one", () => {
    render(<FeedbackLink href="https://forms.gle/beta" labels={labels} />);

    const link = screen.getByRole("link", { name: `${labels.link} ${labels.newTab}` });
    expect(link).toHaveAttribute("href", "https://forms.gle/beta");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  test.each([
    ["no address", ""],
    ["a script, should one ever be stored", "javascript:alert(1)"],
    ["plain http", "http://forms.gle/beta"],
  ])("draws nothing for %s", (_name, href) => {
    const { container } = render(<FeedbackLink href={href} labels={labels} />);

    expect(container).toBeEmptyDOMElement();
  });
});
