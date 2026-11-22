import { render, screen } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { describe, expect, test } from "vitest";

import { useHydrated } from "./use-hydrated";

function Probe() {
  return <p>{useHydrated() ? "hydrated" : "server"}</p>;
}

describe("useHydrated", () => {
  test("is false in the server's markup", () => {
    expect(renderToString(<Probe />)).toContain("server");
  });

  test("is true once the page has hydrated the server's markup", async () => {
    const container = document.createElement("div");
    document.body.append(container);
    container.innerHTML = renderToString(<Probe />);

    render(<Probe />, { container, hydrate: true });

    expect(await screen.findByText("hydrated")).toBeInTheDocument();
    container.remove();
  });
});
