import { describe, expect, test } from "vitest";

import type { GameSchema, GameTable } from "@/lib/api/schema";

import { HEADER_HEIGHT, ROW_HEIGHT, layoutDiagram, type DiagramNode } from "./schema-diagram-layout";

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
      table("staff", ["id", "manager_id"], {
        primary_key: ["id"],
        foreign_keys: [
          { name: "staff_manager", columns: ["manager_id"], referenced_table: "staff", referenced_columns: ["id"] },
        ],
      }),
    ],
  };
}

/** The vertical middle of a column's row, where an arrow meets it. */
function rowMiddle(node: DiagramNode, column: string) {
  return node.y + HEADER_HEIGHT + node.columns.findIndex((c) => c.name === column) * ROW_HEIGHT + ROW_HEIGHT / 2;
}

function expectNoOverlaps(nodes: DiagramNode[]) {
  for (const a of nodes) {
    for (const b of nodes) {
      if (a === b) continue;
      const apart = a.x + a.width <= b.x || b.x + b.width <= a.x || a.y + a.height <= b.y || b.y + b.height <= a.y;
      expect(apart, `${a.name} overlaps ${b.name}`).toBe(true);
    }
  }
}

/** n tables of a dozen columns, each with `keys(i)` as its foreign keys. */
function many(n: number, keys: (i: number) => { to: string; column: string }[]): GameSchema {
  const tables: GameTable[] = [];
  for (let i = 0; i < n; i++) {
    tables.push(
      table(`t${i}`, ["id", ...Array.from({ length: 12 }, (_, c) => `c${c}`)], {
        primary_key: ["id"],
        foreign_keys: keys(i).map(({ to, column }, k) => ({
          name: `t${i}_k${k}`,
          columns: [column],
          referenced_table: to,
          referenced_columns: ["id"],
        })),
      }),
    );
  }
  return { truncated: false, tables };
}

function byName(nodes: DiagramNode[], name: string) {
  const node = nodes.find((n) => n.name === name);
  if (!node) throw new Error(`no node ${name}`);
  return node;
}

describe("the schema diagram's layout", () => {
  test("draws one box per table, tall enough for its columns, without overlaps", () => {
    const { nodes, width, height } = layoutDiagram(hotel());

    expect(nodes.map((n) => n.name)).toEqual(["floors", "rooms", "staff"]);
    for (const node of nodes) {
      expect(node.height).toBe(HEADER_HEIGHT + node.columns.length * ROW_HEIGHT);
      expect(node.x).toBeGreaterThanOrEqual(0);
      expect(node.y).toBeGreaterThanOrEqual(0);
      expect(node.x + node.width).toBeLessThanOrEqual(width);
      expect(node.y + node.height).toBeLessThanOrEqual(height);
    }
    expectNoOverlaps(nodes);
  });

  test("marks primary-key and foreign-key columns", () => {
    const rooms = byName(layoutDiagram(hotel()).nodes, "rooms");

    expect(rooms.columns.map((c) => [c.name, c.primary, c.foreign])).toEqual([
      ["id", true, false],
      ["lvl", false, true],
      ["bld", false, true],
    ]);
  });

  test("a composite key draws one arrow per column pair, from each column to the one it references", () => {
    const { nodes, edges } = layoutDiagram(hotel());
    const rooms = byName(nodes, "rooms");
    const floors = byName(nodes, "floors");

    const edge = edges.find((e) => e.name === "rooms_on_a_floor");
    expect(edge).toMatchObject({ from: "rooms", to: "floors" });
    expect(edge?.links.map((l) => [l.y1, l.y2])).toEqual([
      [rowMiddle(rooms, "lvl"), rowMiddle(floors, "level")],
      [rowMiddle(rooms, "bld"), rowMiddle(floors, "building")],
    ]);
    // Each end sits on a side of its box.
    for (const link of edge?.links ?? []) {
      expect([rooms.x, rooms.x + rooms.width]).toContain(link.x1);
      expect([floors.x, floors.x + floors.width]).toContain(link.x2);
    }
  });

  test("a table that references itself gets a loop on one side", () => {
    const { nodes, edges } = layoutDiagram(hotel());
    const staff = byName(nodes, "staff");

    const [link] = edges.find((e) => e.name === "staff_manager")?.links ?? [];
    expect(link.x1).toBe(link.x2);
    expect(link.y1).toBe(rowMiddle(staff, "manager_id"));
    expect(link.y2).toBe(rowMiddle(staff, "id"));
  });

  // A truncated document can name a table or a column it does not carry.
  test("skips a key to a table the document does not carry, and meets a missing column at the header", () => {
    const schema: GameSchema = {
      truncated: true,
      tables: [
        table("a", ["id"]),
        table("b", ["a_id", "gone_id"], {
          foreign_keys: [
            { name: "to_a", columns: ["a_id"], referenced_table: "a", referenced_columns: ["not_carried"] },
            { name: "to_gone", columns: ["gone_id"], referenced_table: "gone", referenced_columns: ["id"] },
          ],
        }),
      ],
    };

    const { nodes, edges } = layoutDiagram(schema);
    expect(edges.map((e) => e.name)).toEqual(["to_a"]);
    const a = byName(nodes, "a");
    expect(edges[0].links[0].y2).toBe(a.y + HEADER_HEIGHT / 2);
  });

  // An API built before keys were described sends neither list.
  test("reads a document without key lists as a schema without keys", () => {
    const { nodes, edges } = layoutDiagram({ truncated: false, tables: [table("plain", ["x"])] });

    expect(nodes[0].columns[0]).toMatchObject({ primary: false, foreign: false });
    expect(edges).toEqual([]);
  });

  test("an empty schema lays out nothing", () => {
    expect(layoutDiagram({ truncated: false, tables: [] })).toMatchObject({ nodes: [], edges: [] });
  });

  test("widens a box for a long column name and type, up to a bound", () => {
    const long = layoutDiagram({
      truncated: false,
      tables: [
        {
          name: "t",
          columns: [{ name: "x".repeat(63), type: "timestamp with time zone", nullable: true, references: "" }],
        },
        table("u", ["id"]),
      ],
    });

    const [t, u] = long.nodes;
    expect(t.width).toBeGreaterThan(u.width);
    expect(t.width).toBeLessThanOrEqual(480);
  });

  test("a box sized for its longest row draws that row whole", () => {
    const { nodes } = layoutDiagram({
      truncated: false,
      tables: [
        {
          name: "buildings",
          columns: [
            { name: "code", type: "text", nullable: false, references: "" },
            { name: "opened_at", type: "timestamp with time zone", nullable: false, references: "" },
          ],
        },
      ],
    });

    expect(nodes[0].columns.map((c) => [c.label, c.typeLabel])).toEqual([
      ["code", "text"],
      ["opened_at", "timestamp with time zone"],
    ]);
  });

  test("past the widest box, cuts the type before the name", () => {
    const name = "a_column_name_long_enough_to_fill_most_of_it";
    const { nodes } = layoutDiagram({
      truncated: false,
      tables: [{ name: "t", columns: [{ name, type: "timestamp with time zone", nullable: true, references: "" }] }],
    });

    const [column] = nodes[0].columns;
    expect(column.label).toBe(name);
    expect(column.typeLabel.endsWith("…")).toBe(true);
  });

  // The API's bound is 200 tables; the participant waits for this on open.
  test("lays out 200 tables in reasonable time", () => {
    const schema = many(200, (i) =>
      i === 0
        ? []
        : [
            { to: `t${(i * 31 + 7) % i}`, column: "c0" },
            { to: `t${Math.floor(i / 2)}`, column: "c1" },
          ],
    );

    const started = performance.now();
    const { nodes, edges } = layoutDiagram(schema);
    const took = performance.now() - started;

    console.info(`layout of 200 tables and ${edges.length} keys: ${took.toFixed(0)} ms`);
    expect(nodes).toHaveLength(200);
    expect(took).toBeLessThan(5000);
    expectNoOverlaps(nodes);
  });

  // Left to right, a rank is a column; 120 tables in one rank fit the window
  // only as specks.
  test("wraps a star of tables pointing at one into columns", () => {
    const { nodes, width, height } = layoutDiagram(many(121, (i) => (i === 0 ? [] : [{ to: "t0", column: "c0" }])));

    expect(height).toBeLessThan(width * 3);
    expectNoOverlaps(nodes);
  });

  test("past dagre's budget, ranks the tables itself, the arrows running left to right", () => {
    const schema = many(200, (i) =>
      // Ten tables in a chain closed into a cycle, and every other table
      // pointing at all ten: long edges across ranks are what make dagre slow.
      i === 0
        ? [{ to: "t9", column: "c0" }]
        : Array.from({ length: Math.min(i, 10) }, (_, k) => ({ to: `t${k}`, column: `c${k}` })),
    );

    const started = performance.now();
    const { nodes, edges } = layoutDiagram(schema);
    const took = performance.now() - started;

    expect(edges).toHaveLength(1 + 45 + 190 * 10);
    expect(took).toBeLessThan(1000);
    expectNoOverlaps(nodes);
    // Breaking the cycle at t0 turns the keys into it backwards; every other
    // arrow runs left to right.
    for (const edge of edges.filter((e) => e.from !== "t0" && e.to !== "t0")) {
      expect(edge.links.every((l) => l.out1 === 1 && l.out2 === -1 && l.x2 > l.x1), edge.id).toBe(true);
    }
  });
});
