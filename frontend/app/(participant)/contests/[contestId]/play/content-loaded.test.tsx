import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import {
  ContentLoadedProvider,
  ContentLoadedSignal,
  RenderedRefusalSignal,
  useContentLoaded,
  useRenderedRefusal,
} from "./content-loaded";

/** Stands in for the header: prints the two facts it reads from the provider. */
function Reader() {
  const loaded = useContentLoaded();
  const refusal = useRenderedRefusal();
  return (
    <p>
      loaded={String(loaded)} refusal={refusal ?? "none"}
    </p>
  );
}

describe("ContentLoadedProvider", () => {
  test("tells the header nothing outside a provider", () => {
    render(
      <>
        <Reader />
        <ContentLoadedSignal />
        <RenderedRefusalSignal code="contest_not_running" />
      </>,
    );

    expect(screen.getByText("loaded=false refusal=none")).toBeInTheDocument();
  });

  test("carries a loaded workspace to the header", () => {
    render(
      <ContentLoadedProvider>
        <Reader />
        <ContentLoadedSignal />
      </ContentLoadedProvider>,
    );

    expect(screen.getByText("loaded=true refusal=none")).toBeInTheDocument();
  });

  test("carries the refusal the page rendered to the header", () => {
    render(
      <ContentLoadedProvider>
        <Reader />
        <RenderedRefusalSignal code="contest_not_running" />
      </ContentLoadedProvider>,
    );

    expect(screen.getByText("loaded=false refusal=contest_not_running")).toBeInTheDocument();
  });

  // After a refresh the page under the header renders the workspace instead:
  // the refusal it used to show is no longer a fact about the screen.
  test("forgets the refusal once the page stops rendering it", () => {
    const { rerender } = render(
      <ContentLoadedProvider>
        <Reader />
        <RenderedRefusalSignal code="contest_not_running" />
      </ContentLoadedProvider>,
    );

    rerender(
      <ContentLoadedProvider>
        <Reader />
        <ContentLoadedSignal />
      </ContentLoadedProvider>,
    );

    expect(screen.getByText("loaded=true refusal=none")).toBeInTheDocument();
  });
});
