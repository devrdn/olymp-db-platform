import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "./tabs";

function Harness({ defaultValue }: { defaultValue: string }) {
  return (
    <Tabs defaultValue={defaultValue}>
      <TabsList>
        <TabsTrigger value="one">One</TabsTrigger>
        <TabsTrigger value="two">Two</TabsTrigger>
        <TabsTrigger value="three">Three</TabsTrigger>
      </TabsList>
      <TabsContent value="one">Panel one</TabsContent>
      <TabsContent value="two">Panel two</TabsContent>
      <TabsContent value="three">Panel three</TabsContent>
    </Tabs>
  );
}

describe("the hand-rolled tabs", () => {
  // State inside a hidden panel survives only if the panel stays in the DOM.
  test("keeps every panel in the DOM, hiding the ones not selected with the hidden attribute", () => {
    render(<Harness defaultValue="one" />);

    expect(screen.getByText("Panel one")).toBeVisible();
    expect(screen.getByText("Panel two")).not.toBeVisible();
    expect(screen.getByText("Panel three")).not.toBeVisible();
    expect(screen.getByText("Panel two").closest('[role="tabpanel"]')).toHaveAttribute("hidden");
  });

  test("marks exactly the selected tab aria-selected", async () => {
    render(<Harness defaultValue="one" />);

    expect(screen.getByRole("tab", { name: "One" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "Two" })).toHaveAttribute("aria-selected", "false");

    await userEvent.click(screen.getByRole("tab", { name: "Two" }));

    expect(screen.getByRole("tab", { name: "One" })).toHaveAttribute("aria-selected", "false");
    expect(screen.getByRole("tab", { name: "Two" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Panel two")).toBeVisible();
    expect(screen.getByText("Panel one")).not.toBeVisible();
  });

  test("pairs each tab to its panel with aria-controls / aria-labelledby", () => {
    render(<Harness defaultValue="one" />);

    const tab = screen.getByRole("tab", { name: "One" });
    const panel = screen.getByText("Panel one").closest('[role="tabpanel"]')!;

    expect(tab).toHaveAttribute("aria-controls", panel.id);
    expect(panel).toHaveAttribute("aria-labelledby", tab.id);
  });

  test("only the selected tab is in the tab order", () => {
    render(<Harness defaultValue="two" />);

    expect(screen.getByRole("tab", { name: "One" })).toHaveAttribute("tabindex", "-1");
    expect(screen.getByRole("tab", { name: "Two" })).toHaveAttribute("tabindex", "0");
    expect(screen.getByRole("tab", { name: "Three" })).toHaveAttribute("tabindex", "-1");
  });

  test("ArrowRight and ArrowLeft move focus between tabs, wrapping at the ends", async () => {
    render(<Harness defaultValue="one" />);
    screen.getByRole("tab", { name: "One" }).focus();

    await userEvent.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "Two" })).toHaveFocus();

    await userEvent.keyboard("{ArrowRight}{ArrowRight}");
    expect(screen.getByRole("tab", { name: "One" })).toHaveFocus();

    await userEvent.keyboard("{ArrowLeft}");
    expect(screen.getByRole("tab", { name: "Three" })).toHaveFocus();
  });

  test("Home and End jump to the first and last tab", async () => {
    render(<Harness defaultValue="two" />);
    screen.getByRole("tab", { name: "Two" }).focus();

    await userEvent.keyboard("{End}");
    expect(screen.getByRole("tab", { name: "Three" })).toHaveFocus();

    await userEvent.keyboard("{Home}");
    expect(screen.getByRole("tab", { name: "One" })).toHaveFocus();
  });

  test("arrow-key focus movement does not select a tab on its own", async () => {
    render(<Harness defaultValue="one" />);
    screen.getByRole("tab", { name: "One" }).focus();

    await userEvent.keyboard("{ArrowRight}");

    expect(screen.getByRole("tab", { name: "Two" })).toHaveFocus();
    expect(screen.getByRole("tab", { name: "One" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Panel one")).toBeVisible();
  });

  // Native `<button>` behaviour, not wired by hand.
  test("Enter activates the focused tab", async () => {
    render(<Harness defaultValue="one" />);
    screen.getByRole("tab", { name: "One" }).focus();
    await userEvent.keyboard("{ArrowRight}{Enter}");

    expect(screen.getByRole("tab", { name: "Two" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Panel two")).toBeVisible();
  });
});
