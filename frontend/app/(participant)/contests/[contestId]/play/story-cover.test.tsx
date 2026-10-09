import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { StoryCover } from "./story-cover";

const CONTEST = "0198c3a0-6d1e-7c8b-9f22-2b6b1f0c4a11";

/**
 * SPEC.md §10's promises for the cover: the rendition asked for, the
 * system's processing, a title readable over any picture, and a drawn cover
 * when there is no photograph.
 */
describe("the cover above a story", () => {
  test("shows the photograph the contest wears, at the rendition this screen is sized for", () => {
    render(
      <StoryCover
        contestId={CONTEST}
        title="The Warehouse Fire"
        coverHash="9f86d081884c7d65"
        coverAttribution="Photo: A. Organiser, CC BY 4.0"
        dict={en}
      />,
    );

    const picture = screen.getByRole("img", { name: "Cover of The Warehouse Fire" });
    // 1600 is this surface's rendition; cards take 800.
    expect(picture).toHaveAttribute("src", expect.stringContaining("size=1600"));
    // The hash makes a replaced cover a different address.
    expect(picture).toHaveAttribute("src", expect.stringContaining("v=9f86d081884c7d65"));
  });

  /** Under a timer the picture must stay off the critical path and must not shift the story. */
  test("loads the photograph late and reserves its box first", () => {
    render(
      <StoryCover
        contestId={CONTEST}
        title="The Warehouse Fire"
        coverHash="9f86d081884c7d65"
        coverAttribution="Photo: A. Organiser, CC BY 4.0"
        dict={en}
      />,
    );

    const picture = screen.getByRole("img", { name: "Cover of The Warehouse Fire" });
    expect(picture).toHaveAttribute("loading", "lazy");
    expect(picture).toHaveAttribute("decoding", "async");
    expect(picture).toHaveAttribute("width", "1600");
    expect(picture).toHaveAttribute("height", "900");
  });

  // SPEC.md §10.1: the `.photograph` utility applies the system's processing.
  test("processes the photograph the way the system says it is processed", () => {
    render(
      <StoryCover
        contestId={CONTEST}
        title="The Warehouse Fire"
        coverHash="9f86d081884c7d65"
        coverAttribution="Photo: A. Organiser, CC BY 4.0"
        dict={en}
      />,
    );

    expect(screen.getByRole("img", { name: "Cover of The Warehouse Fire" })).toHaveClass("photograph");
  });

  // SPEC.md §10.1: an uploaded picture is published only with its credit.
  test("credits whoever made the photograph", () => {
    render(
      <StoryCover
        contestId={CONTEST}
        title="The Warehouse Fire"
        coverHash="9f86d081884c7d65"
        coverAttribution="Photo: A. Organiser, CC BY 4.0"
        dict={en}
      />,
    );

    expect(screen.getByText("Photo: A. Organiser, CC BY 4.0")).toBeInTheDocument();
  });

  /** SPEC.md §10.3: the drawn cover fills the same 16:9 box, so nothing above the story moves. */
  test("wears a drawn cover when nobody uploaded a photograph", () => {
    const { container } = render(
      <StoryCover contestId={CONTEST} title="The Warehouse Fire" coverHash="" coverAttribution="" dict={en} />,
    );

    expect(container.querySelector("img")).toBeNull();
    // The drawn cover's geometry, which nothing else here draws.
    expect(container.querySelector("svg")).not.toBeNull();
    expect(screen.getByText("The Warehouse Fire")).toBeInTheDocument();
  });

  test("draws the same contest the same way twice", () => {
    const first = render(
      <StoryCover contestId={CONTEST} title="The Warehouse Fire" coverHash="" coverAttribution="" dict={en} />,
    );
    const second = render(
      <StoryCover contestId={CONTEST} title="The Warehouse Fire" coverHash="" coverAttribution="" dict={en} />,
    );

    expect(first.container.innerHTML).toBe(second.container.innerHTML);
  });

  // A drawing has nobody to credit.
  test("credits nobody for a drawn cover", () => {
    render(
      <StoryCover
        contestId={CONTEST}
        title="The Warehouse Fire"
        coverHash=""
        coverAttribution="Photo: A. Organiser, CC BY 4.0"
        dict={en}
      />,
    );

    expect(screen.queryByText("Photo: A. Organiser, CC BY 4.0")).not.toBeInTheDocument();
  });

  /**
   * SPEC.md §10.2: the scrim gives the title its contrast whatever the picture. The
   * title is a real heading, so screen readers get it; the scrim is hidden
   * from them.
   */
  test("prints the title on a scrim of the page's own ground, not on the picture", () => {
    const { container } = render(
      <StoryCover
        contestId={CONTEST}
        title="The Warehouse Fire"
        coverHash="9f86d081884c7d65"
        coverAttribution="Photo: A. Organiser, CC BY 4.0"
        dict={en}
      />,
    );

    expect(screen.getByRole("heading", { name: "The Warehouse Fire" })).toBeInTheDocument();
    const scrim = container.querySelector("[aria-hidden='true'][class*='scrim']");
    expect(scrim).not.toBeNull();
  });
});
