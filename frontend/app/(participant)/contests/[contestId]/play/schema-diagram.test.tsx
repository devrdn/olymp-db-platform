import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { GameSchema, GameTable } from "@/lib/api/schema";

import SchemaDiagram from "./schema-diagram";

const t = en.participant.play.schema.diagram;

function table(name: string, columns: string[], extra: Partial<GameTable> = {}): GameTable {
  return {
    name,
    columns: columns.map((column) => ({ name: column, type: "integer", nullable: false, references: "" })),
    ...extra,
  };
}

function hotel(): GameSchema {
  return {
    truncated: false,
    tables: [
      table("floors", ["building", "level"], { primary_key: ["level", "building"] }),
      table("rooms", ["id", "lvl", "bld"], {
        primary_key: ["id"],
        foreign_keys: [
          {
            name: "rooms_on_a_floor",
            columns: ["lvl", "bld"],
            referenced_table: "floors",
            referenced_columns: ["level", "building"],
          },
        ],
      }),
      table("notes", ["body"]),
    ],
  };
}

function draw(schema: GameSchema = hotel()) {
  return render(<SchemaDiagram schema={schema} t={t} truncatedNote={en.participant.play.schema.truncated} />);
}

/** The pan and zoom the drawing is shown at, read from its transform. */
function view(container: HTMLElement) {
  const transform = container.querySelector("[data-viewport]")?.getAttribute("transform") ?? "";
  const [, x, y, k] = /translate\(([-\d.]+) ([-\d.]+)\) scale\(([-\d.]+)\)/.exec(transform) ?? [];
  return { x: Number(x), y: Number(y), k: Number(k) };
}

function canvas() {
  return screen.getByRole("application", { name: t.canvas });
}

describe("the schema diagram", () => {
  test("draws a box per table with its columns, marking primary and foreign keys", () => {
    const { container } = draw();

    const boxes = container.querySelectorAll("[data-table]");
    expect([...boxes].map((box) => box.getAttribute("data-table"))).toEqual(["floors", "rooms", "notes"]);

    const rooms = container.querySelector('[data-table="rooms"]') as HTMLElement;
    expect(within(rooms).getByText("rooms")).toBeInTheDocument();
    const marks = [...rooms.querySelectorAll("[data-column]")].map((row) => [
      row.getAttribute("data-column"),
      row.querySelector("[data-mark]")?.textContent ?? "",
    ]);
    expect(marks).toEqual([
      ["id", "PK"],
      ["lvl", "FK"],
      ["bld", "FK"],
    ]);
    expect(within(rooms).getAllByText("integer")).toHaveLength(3);
  });

  test("draws an arrow from each foreign-key column to the column it references", () => {
    const { container } = draw();

    const edge = container.querySelector('[data-from="rooms"][data-edge="rooms_on_a_floor"]');
    expect(edge?.querySelectorAll("path")).toHaveLength(2);
    for (const path of edge?.querySelectorAll("path") ?? []) {
      expect(path.getAttribute("d")).toMatch(/^M [\d.]+ [\d.]+ C /);
      expect(path.getAttribute("marker-end")).toMatch(/^url\(#/);
    }
  });

  // The drawing is hidden from assistive technology; this is what it hears.
  test("lists the relations and the primary keys as text", () => {
    draw();

    const relations = screen.getByRole("list", { name: t.relations });
    expect(within(relations).getAllByRole("listitem").map((item) => item.textContent)).toEqual([
      "rooms (lvl, bld) references floors (level, building)",
    ]);
    const keys = screen.getByRole("list", { name: t.primaryKeys });
    expect(within(keys).getAllByRole("listitem").map((item) => item.textContent)).toEqual([
      "floors: level, building",
      "rooms: id",
    ]);
  });

  test("says when no table references another", () => {
    draw({ truncated: false, tables: [table("notes", ["body"])] });

    expect(screen.getByText(t.noRelations)).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: t.relations })).not.toBeInTheDocument();
  });

  test("an empty schema shows an empty state and no canvas", () => {
    draw({ truncated: false, tables: [] });

    expect(screen.getByText(t.empty)).toBeInTheDocument();
    expect(screen.queryByRole("application")).not.toBeInTheDocument();
  });

  test("says when the schema is not drawn whole", () => {
    draw({ ...hotel(), truncated: true });

    expect(screen.getByText(en.participant.play.schema.truncated)).toBeInTheDocument();
  });

  test("the canvas is reachable by keyboard and describes its keys", async () => {
    draw();

    await userEvent.tab();
    expect(canvas()).toHaveFocus();
    expect(canvas()).toHaveAccessibleDescription(t.keys);
  });

  test("the arrow keys pan, + and − zoom, and 0 fits the drawing again", async () => {
    const user = userEvent.setup();
    const { container } = draw();
    canvas().focus();
    const fitted = view(container);

    await user.keyboard("{ArrowRight}");
    expect(view(container).x).toBeLessThan(fitted.x);
    await user.keyboard("{ArrowDown}");
    expect(view(container).y).toBeLessThan(fitted.y);

    await user.keyboard("+");
    expect(view(container).k).toBeGreaterThan(fitted.k);
    await user.keyboard("--");
    expect(view(container).k).toBeLessThan(fitted.k);

    await user.keyboard("0");
    expect(view(container)).toEqual(fitted);
  });

  test("the zoom buttons zoom and fit", async () => {
    const user = userEvent.setup();
    const { container } = draw();
    const fitted = view(container);

    await user.click(screen.getByRole("button", { name: t.zoomIn }));
    expect(view(container).k).toBeGreaterThan(fitted.k);
    await user.click(screen.getByRole("button", { name: t.zoomOut }));
    await user.click(screen.getByRole("button", { name: t.zoomOut }));
    expect(view(container).k).toBeLessThan(fitted.k);
    await user.click(screen.getByRole("button", { name: t.fit }));
    expect(view(container)).toEqual(fitted);
  });

  test("dragging pans and the wheel zooms", () => {
    const { container } = draw();
    const fitted = view(container);

    fireEvent.pointerDown(canvas(), { button: 0, clientX: 100, clientY: 100, pointerId: 1 });
    fireEvent.pointerMove(canvas(), { clientX: 140, clientY: 70, pointerId: 1 });
    fireEvent.pointerUp(canvas(), { clientX: 140, clientY: 70, pointerId: 1 });
    expect(view(container).x).toBeCloseTo(fitted.x + 40);
    expect(view(container).y).toBeCloseTo(fitted.y - 30);

    fireEvent.wheel(canvas(), { deltaY: -200, clientX: 10, clientY: 10 });
    expect(view(container).k).toBeGreaterThan(fitted.k);
  });

  // The API's bound is 200 tables of 200 columns: forty thousand rows.
  describe("a large schema", () => {
    function large(): GameSchema {
      const tables = Array.from({ length: 200 }, (_, i) =>
        table(
          `t${i}`,
          Array.from({ length: 40 }, (_, c) => `c${c}`),
          i === 0 ? {} : { foreign_keys: [{ name: "up", columns: ["c1"], referenced_table: `t${i - 1}`, referenced_columns: ["c0"] }] },
        ),
      );
      return { truncated: false, tables };
    }

    test("fitted to the window, draws every box without its column rows", () => {
      const { container } = draw(large());

      expect(container.querySelectorAll("[data-table]")).toHaveLength(200);
      expect(container.querySelectorAll("[data-column]")).toHaveLength(0);
    });

    test("zoomed in, draws only the boxes in view, with their columns", async () => {
      const user = userEvent.setup();
      const { container } = draw(large());
      canvas().focus();

      // From about 2% fitted to past the zoom where column rows are drawn.
      for (let i = 0; i < 15; i++) await user.keyboard("+");

      const drawn = container.querySelectorAll("[data-table]").length;
      expect(drawn).toBeGreaterThan(0);
      expect(drawn).toBeLessThan(20);
      expect(container.querySelectorAll("[data-column]").length).toBeGreaterThan(0);
    });
  });
});
