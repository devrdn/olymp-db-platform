import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import { StoryCover } from "./story-cover";

const CONTEST = "0198c3a0-6d1e-7c8b-9f22-2b6b1f0c4a11";

/**
 * The one place a photograph does work (design spec §10): above the crime
 * story, on the screen a participant sits in front of for two hours.
 *
 * What is asserted here is what §10 promises and not how it is drawn — the
 * rendition asked for, the processing the system applies rather than the
 * author, the title's independence from whatever is in the bottom third of
 * somebody else's picture, and the fact that a contest with no photograph is
 * not a hole above its story.
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
    // 1600 is the rendition stored for this surface; the card's own is 800.
    expect(picture).toHaveAttribute("src", expect.stringContaining("size=1600"));
    // The hash travels so a replaced cover is a different address.
    expect(picture).toHaveAttribute("src", expect.stringContaining("v=9f86d081884c7d65"));
  });

  /**
   * The participant's screen is under a timer: the picture may not be on the
   * critical path, and it may not move the story out from under a finger
   * that is already reading it.
   */
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

  // The processing is the system's, not the author's (§10.1): the `.photograph`
  // utility carries saturate(0.45) contrast(1.06) brightness(0.99).
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

  // §10.1: an uploaded picture is not published without the line saying whose.
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

  /**
   * §10.3: a contest with no photograph is not a hole above its story. It
   * wears the drawn cover of the same family, in the same 16:9 box, so
   * nothing above the story moves between one contest and the next.
   */
  test("wears a drawn cover when nobody uploaded a photograph", () => {
    const { container } = render(
      <StoryCover contestId={CONTEST} title="The Warehouse Fire" coverHash="" coverAttribution="" dict={en} />,
    );

    expect(container.querySelector("img")).toBeNull();
    // The drawn cover's own geometry, which nothing else on this screen draws.
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

  // A drawing has nobody to credit: its author is us.
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
   * §10.2, and the reason the scrim exists at all: the organiser picks the
   * subject and the system owes the title its contrast whatever they picked.
   * The title is a heading in the document, not a caption baked into the
   * picture, so a reader using their ears gets it too — and the scrim itself
   * is scenery, hidden from them.
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
