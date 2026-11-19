import { render, screen } from "@testing-library/react";
import { createRef } from "react";
import { describe, expect, test } from "vitest";

import { ScrollBox } from "./scroll-box";

describe("the scroll box", () => {
  // jsdom has no layout, so the contract is the class; `ScrollBox` says why it
  // is `relative`.
  test("scrolls sideways and is the containing block of what it holds", () => {
    render(
      <ScrollBox data-testid="box">
        <table>
          <tbody>
            <tr>
              <td>
                <span className="sr-only">Remove</span>
              </td>
            </tr>
          </tbody>
        </table>
      </ScrollBox>,
    );

    expect(screen.getByTestId("box")).toHaveClass("relative", "overflow-x-auto");
    expect(screen.getByRole("table").parentElement).toBe(screen.getByTestId("box"));
  });

  test("takes a box that scrolls both ways and keeps it positioned", () => {
    render(<ScrollBox data-testid="box" className="max-h-96 overflow-auto" />);

    const box = screen.getByTestId("box");
    expect(box).toHaveClass("relative", "overflow-auto", "max-h-96");
    expect(box).not.toHaveClass("overflow-x-auto");
  });

  test("passes the ref and the scroll handler through to the element", () => {
    const ref = createRef<HTMLDivElement>();
    let scrolled = false;
    render(<ScrollBox ref={ref} data-testid="box" onScroll={() => (scrolled = true)} />);

    expect(ref.current).toBe(screen.getByTestId("box"));
    ref.current?.dispatchEvent(new Event("scroll"));
    expect(scrolled).toBe(true);
  });
});
