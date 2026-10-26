import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import ro from "@/lib/i18n/dictionaries/ro";
import ru from "@/lib/i18n/dictionaries/ru";
import type { GameSchema } from "@/lib/api/schema";

import { SchemaDiagramDialog } from "./schema-diagram-dialog";

const t = en.participant.play.schema.diagram;

const schema: GameSchema = {
  truncated: false,
  tables: [
    {
      name: "rooms",
      columns: [{ name: "id", type: "integer", nullable: false, references: "" }],
      primary_key: ["id"],
      foreign_keys: [],
    },
  ],
};

function opener() {
  return screen.getByRole("button", { name: t.openLabel });
}

describe("the schema diagram's dialog", () => {
  test("opens a large view titled as the diagram, drawing the schema once loaded", async () => {
    const user = userEvent.setup();
    render(<SchemaDiagramDialog schema={schema} contestId="c1" dict={en} />);

    expect(opener()).toHaveTextContent(t.open);
    await user.click(opener());

    const dialog = await screen.findByRole("dialog", { name: t.title });
    expect(dialog).toHaveAccessibleDescription(t.description);
    expect(await within(dialog).findByRole("application", { name: t.canvas })).toBeInTheDocument();
    expect(within(dialog).getByText("rooms: id")).toBeInTheDocument();
  });

  // The boxes draw "PK" and "FK" in every language, so every description says what they mean.
  test.each([
    ["en", en],
    ["ro", ro],
    ["ru", ru],
  ])("the %s description explains the PK and FK marks", (_, dict) => {
    expect(dict.participant.play.schema.diagram.description).toMatch(/PK.*FK/);
  });

  test("keeps focus inside while open and hands it back to the button on Escape", async () => {
    const user = userEvent.setup();
    render(
      <>
        <SchemaDiagramDialog schema={schema} contestId="c1" dict={en} />
        <button type="button">outside</button>
      </>,
    );

    opener().focus();
    await user.keyboard("{Enter}");
    const dialog = await screen.findByRole("dialog", { name: t.title });
    await within(dialog).findByRole("application", { name: t.canvas });

    // Base UI's focus guards hand focus back inside after a frame.
    for (let i = 0; i < 8; i++) {
      await user.tab();
      await waitFor(() => expect(dialog).toContainElement(document.activeElement as HTMLElement));
    }

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(opener()).toHaveFocus();
  });

  test("the corner button closes it", async () => {
    const user = userEvent.setup();
    render(<SchemaDiagramDialog schema={schema} contestId="c1" dict={en} />);

    await user.click(opener());
    const dialog = await screen.findByRole("dialog", { name: t.title });
    await user.click(within(dialog).getByRole("button", { name: t.close }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  test("an empty schema opens to the empty state", async () => {
    const user = userEvent.setup();
    render(<SchemaDiagramDialog schema={{ truncated: false, tables: [] }} contestId="c1" dict={en} />);

    await user.click(opener());

    expect(await screen.findByText(t.empty)).toBeInTheDocument();
    // A sentence does not need a window the size of the screen.
    expect(screen.getByRole("dialog")).not.toHaveClass("max-w-none");
  });
});
