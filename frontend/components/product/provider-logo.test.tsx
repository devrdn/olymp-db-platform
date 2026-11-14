import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { PROVIDER_IDS } from "@/lib/auth/external";

import { ProviderLogo } from "./provider-logo";

describe("ProviderLogo", () => {
  test("draws Microsoft's four squares in exactly its four colours", () => {
    const { container } = render(<ProviderLogo provider="microsoft" />);

    const fills = [...container.querySelectorAll("rect")].map((rect) => rect.getAttribute("fill"));
    expect(fills).toEqual(["#F25022", "#7FBA00", "#00A4EF", "#FFB900"]);
  });

  test("shows Google's G as a hidden image with no tile behind it", () => {
    const { container } = render(<ProviderLogo provider="google" />);

    const img = container.querySelector("img");
    expect(img).toHaveAttribute("src", "/brand/google-g.png");
    expect(img).toHaveAttribute("alt", "");
    expect(img).toHaveAttribute("aria-hidden", "true");
    expect(container.firstElementChild).toBe(img);
    expect(img).not.toHaveClass("bg-white");
  });

  test("masks GitHub's mark over the text colour", () => {
    const { container } = render(<ProviderLogo provider="github" />);

    const mark = container.firstElementChild;
    expect(mark).toHaveAttribute("aria-hidden", "true");
    expect(mark).toHaveClass("bg-current");
    expect(mark?.getAttribute("style")).toContain("/brand/github-mark.svg");
  });

  test.each(PROVIDER_IDS)("keeps the %s mark out of the accessibility tree and the tab order", (provider) => {
    const { container } = render(<ProviderLogo provider={provider} />);

    expect(container.firstElementChild).toHaveAttribute("aria-hidden", "true");
    expect(container.querySelectorAll("a, button, input, [tabindex]")).toHaveLength(0);
    for (const svg of container.querySelectorAll("svg")) {
      expect(svg.getAttribute("focusable")).toBe("false");
    }
  });
});
