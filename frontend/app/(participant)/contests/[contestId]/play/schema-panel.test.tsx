import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { GameSchema } from "@/lib/api/schema";

import { SchemaPanel } from "./schema-panel";

function schema(overrides: Partial<GameSchema> = {}): GameSchema {
  return {
    truncated: false,
    tables: [
      {
        name: "guests",
        columns: [
          { name: "id", type: "uuid", nullable: false, references: "" },
          { name: "full_name", type: "text", nullable: false, references: "" },
          { name: "room_id", type: "uuid", nullable: true, references: "rooms" },
        ],
      },
      {
        name: "rooms",
        columns: [{ name: "id", type: "uuid", nullable: false, references: "" }],
      },
    ],
    ...overrides,
  };
}

function panel(value: GameSchema = schema()) {
  return render(<SchemaPanel schema={value} dict={en} />);
}

describe("the schema panel", () => {
  test("shows the tables, their columns and the type of each", () => {
    panel();

    expect(screen.getByRole("button", { name: /guests/ })).toBeInTheDocument();
    expect(screen.getByText("full_name")).toBeInTheDocument();
    expect(screen.getByText("text")).toBeInTheDocument();
  });

  // The join a participant needs is exactly this. Having to discover it by
  // guessing is not the puzzle the olympiad is setting.
  test("names the table a foreign key points at, instead of the column's type", () => {
    panel();

    expect(screen.getByText("fk rooms")).toBeInTheDocument();
    // And says so on the table itself, the way the design draws it.
    expect(within(screen.getByRole("button", { name: /guests/ })).getByText("fk 1")).toBeInTheDocument();
  });

  test("a table can be collapsed and opened again", async () => {
    const user = userEvent.setup();
    panel();

    const guests = screen.getByRole("button", { name: /guests/ });
    expect(guests).toHaveAttribute("aria-expanded", "true");

    await user.click(guests);
    expect(guests).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("full_name")).not.toBeInTheDocument();

    await user.click(guests);
    expect(screen.getByText("full_name")).toBeInTheDocument();
  });

  test("searching narrows to the matching columns", async () => {
    const user = userEvent.setup();
    panel();

    await user.type(screen.getByLabelText(en.participant.play.schema.searchLabel), "full");

    expect(screen.getByText("full_name")).toBeInTheDocument();
    // `rooms` matched nothing, so it is gone entirely — as is `guests`'s own
    // `id`, which the search did not match.
    expect(screen.queryByRole("button", { name: /rooms/ })).not.toBeInTheDocument();
    expect(screen.queryByText("room_id")).not.toBeInTheDocument();
  });

  // A hit the participant cannot see is not a hit: a column matched inside a
  // collapsed table has to open it.
  test("a match inside a collapsed table opens it", async () => {
    const user = userEvent.setup();
    panel();

    const guests = screen.getByRole("button", { name: /guests/ });
    await user.click(guests);
    expect(screen.queryByText("full_name")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText(en.participant.play.schema.searchLabel), "full");
    expect(screen.getByText("full_name")).toBeInTheDocument();
  });

  test("a table matched by its own name keeps all of its columns", async () => {
    const user = userEvent.setup();
    panel();

    await user.type(screen.getByLabelText(en.participant.play.schema.searchLabel), "guests");

    expect(screen.getByText("id")).toBeInTheDocument();
    expect(screen.getByText("full_name")).toBeInTheDocument();
    expect(screen.getByText("room_id")).toBeInTheDocument();
  });

  test("says so when nothing matches, rather than looking empty", async () => {
    const user = userEvent.setup();
    panel();

    await user.type(screen.getByLabelText(en.participant.play.schema.searchLabel), "zzz");

    expect(screen.getByText(en.participant.play.schema.nothingFound)).toBeInTheDocument();
  });

  test("says when the schema shown is not the whole one", () => {
    panel(schema({ truncated: true }));

    expect(screen.getByText(en.participant.play.schema.truncated)).toBeInTheDocument();
  });

  // The participant's hands are in the editor, so the shortcut is bound on
  // the document rather than on the panel.
  test("⌘K puts the cursor in the search field from anywhere on the screen", async () => {
    const user = userEvent.setup();
    panel();

    document.body.focus();
    await user.keyboard("{Meta>}k{/Meta}");

    expect(screen.getByLabelText(en.participant.play.schema.searchLabel)).toHaveFocus();
  });

  // Forty thousand rows laid out at once is a frame budget nothing recovers
  // from, and the API's own bound allows exactly that.
  test("a very large schema starts collapsed instead of laying every column out", () => {
    const many: GameSchema = {
      truncated: false,
      tables: Array.from({ length: 60 }, (_, table) => ({
        name: `t${table}`,
        columns: Array.from({ length: 20 }, (_, column) => ({
          name: `c${column}`,
          type: "text",
          nullable: false,
          references: "",
        })),
      })),
    };
    panel(many);

    expect(screen.getByRole("button", { name: /t0/ })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("c0")).not.toBeInTheDocument();
  });

  test("an empty database says so", () => {
    panel(schema({ tables: [] }));

    expect(screen.getByText(en.participant.play.schema.empty)).toBeInTheDocument();
  });
});
