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

  test.each(["google", "github"] as const)("renders a hidden icon for %s", (provider) => {
    const { container } = render(<ProviderLogo provider={provider} />);

    const svg = container.querySelector("svg");
    expect(svg).not.toBeNull();
    expect(svg).toHaveAttribute("aria-hidden", "true");
  });

  test.each(PROVIDER_IDS)("keeps the %s mark out of the accessibility tree and the tab order", (provider) => {
    const { container } = render(<ProviderLogo provider={provider} />);

    const svgs = container.querySelectorAll("svg");
    expect(svgs).toHaveLength(1);
    for (const svg of svgs) {
      expect(svg).toHaveAttribute("aria-hidden", "true");
      expect(svg.getAttribute("focusable")).toBe("false");
    }
  });
});
