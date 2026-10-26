import dagre from "@dagrejs/dagre";
import { afterEach, describe, expect, test, vi } from "vitest";

import type { GameSchema, GameTable } from "@/lib/api/schema";

import {
  HEADER_HEIGHT,
  ROW_HEIGHT,
  layoutDiagram,
  linkPath,
  routeEdges,
  type Diagram,
  type DiagramNode,
} from "./schema-diagram-layout";

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

/**
 * Who was seen where: `sightings` references `suspects` and `rooms`, and
 * `suspects` references `rooms`, so the key from sightings to rooms skips the
 * rank suspects stands in.
 */
function sightings(): GameSchema {
  const key = (name: string, column: string, to: string) => ({
    name,
    columns: [column],
    referenced_table: to,
    referenced_columns: ["id"],
  });
  return {
    truncated: false,
    tables: [
      table("rooms", ["id", "name"], { primary_key: ["id"] }),
      table("suspects", ["id", "name", "alibi", "room_id"], {
        primary_key: ["id"],
        foreign_keys: [key("suspects_room_id_fkey", "room_id", "rooms")],
      }),
      table("sightings", ["id", "suspect_id", "room_id", "seen_at"], {
        primary_key: ["id"],
        foreign_keys: [
          key("sightings_suspect_id_fkey", "suspect_id", "suspects"),
          key("sightings_room_id_fkey", "room_id", "rooms"),
        ],
      }),
      table("CaseFiles", ["id", "note"], { primary_key: ["id"] }),
    ],
  };
}

/** Points along an arrow's path ("M x y" then cubic "C" segments), ends left out. */
function pointsAlong(path: string): { x: number; y: number }[] {
  const numbers = path.match(/-?\d+(\.\d+)?/g)!.map(Number);
  let [x0, y0] = numbers;
  const points: { x: number; y: number }[] = [];
  for (let i = 2; i + 5 < numbers.length; i += 6) {
    const [ax, ay, bx, by, x3, y3] = numbers.slice(i, i + 6);
    for (let step = 1; step < 20; step++) {
      const t = step / 20;
      const u = 1 - t;
      points.push({
        x: u * u * u * x0 + 3 * u * u * t * ax + 3 * u * t * t * bx + t * t * t * x3,
        y: u * u * u * y0 + 3 * u * u * t * ay + 3 * u * t * t * by + t * t * t * y3,
      });
    }
    [x0, y0] = [x3, y3];
  }
  return points;
}

function byName(nodes: DiagramNode[], name: string) {
  const node = nodes.find((n) => n.name === name);
  if (!node) throw new Error(`no node ${name}`);
  return node;
}

type Key = [column: string, to: string];

/** Tables with an `id` primary key, their columns and their keys to other tables' `id`. */
function schemaOf(specs: [name: string, columns: string[], keys?: Key[]][]): GameSchema {
  return {
    truncated: false,
    tables: specs.map(([name, columns, keys = []]) =>
      table(name, ["id", ...columns], {
        primary_key: ["id"],
        foreign_keys: keys.map(([column, to]) => ({
          name: `${name}_${column}_fkey`,
          columns: [column],
          referenced_table: to,
          referenced_columns: ["id"],
        })),
      }),
    ),
  };
}

/** Each table referencing the next: a chain of six. */
function chain(): GameSchema {
  return schemaOf([
    ["residents", ["name", "born_at", "apartment_id"], [["apartment_id", "apartments"]]],
    ["apartments", ["number", "floor", "building_id"], [["building_id", "buildings"]]],
    ["buildings", ["address", "built_at", "street_id"], [["street_id", "streets"]]],
    ["streets", ["name", "district_id"], [["district_id", "districts"]]],
    ["districts", ["name", "city_id"], [["city_id", "cities"]]],
    ["cities", ["name", "population"]],
  ]);
}

/** One fact table referencing seven dimensions, of different widths. */
function star(): GameSchema {
  const dimensions = ["dates", "products", "stores", "customers", "promotions", "employees", "channels"];
  return schemaOf([
    ["sales", ["amount", ...dimensions.map((d) => `${d}_id`)], dimensions.map((d) => [`${d}_id`, d] as Key)],
    ["dates", ["day", "month", "year", "weekday"]],
    ["products", ["name", "brand", "price"]],
    ["stores", ["name", "city", "opened_timestamp_with_zone"]],
    ["customers", ["name", "email"]],
    ["promotions", ["name", "discount"]],
    ["employees", ["name"]],
    ["channels", ["name"]],
  ]);
}

/** A star whose dimensions have dimensions of their own, two sharing one. */
function snowflake(): GameSchema {
  return schemaOf([
    ["sales", ["amount", "date_id", "product_id", "store_id", "customer_id"], [["date_id", "dates"], ["product_id", "products"], ["store_id", "stores"], ["customer_id", "customers"]]],
    ["dates", ["day", "month", "year"]],
    ["products", ["name", "category_id"], [["category_id", "categories"]]],
    ["categories", ["name", "department_id"], [["department_id", "departments"]]],
    ["departments", ["name"]],
    ["stores", ["name", "city_id"], [["city_id", "cities"]]],
    ["customers", ["name", "email", "city_id"], [["city_id", "cities"]]],
    ["cities", ["name", "country_id"], [["country_id", "countries"]]],
    ["countries", ["name", "code"]],
  ]);
}

/** Arrows of different keys that cross each other, judged along their drawn paths. */
function crossings({ edges }: Diagram): number {
  const lines = edges.flatMap((edge) => edge.links.map((link) => ({ edge, points: pointsAlong(linkPath(link)) })));
  const side = (p: Point, q: Point, r: Point) => Math.sign((q.x - p.x) * (r.y - p.y) - (q.y - p.y) * (r.x - p.x));
  const cross = (a: Point, b: Point, c: Point, d: Point) => side(a, b, c) * side(a, b, d) < 0 && side(c, d, a) * side(c, d, b) < 0;
  let count = 0;
  for (let i = 0; i < lines.length; i++) {
    for (let j = i + 1; j < lines.length; j++) {
      if (lines[i].edge === lines[j].edge) continue;
      const [a, b] = [lines[i].points, lines[j].points];
      if (a.some((p, m) => m > 0 && b.some((q, n) => n > 0 && cross(a[m - 1], p, b[n - 1], q)))) count++;
    }
  }
  return count;
}

type Point = { x: number; y: number };

function expectNoArrowThroughATable({ nodes, edges }: Diagram) {
  for (const edge of edges) {
    for (const link of edge.links) {
      for (const node of nodes) {
        if (node.name === edge.from || node.name === edge.to) continue;
        const inside = pointsAlong(linkPath(link)).filter(
          ({ x, y }) => x > node.x && x < node.x + node.width && y > node.y && y < node.y + node.height,
        );
        expect(inside, `${edge.name} passes through ${node.name}`).toEqual([]);
      }
    }
  }
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

  test("an arrow that skips a rank goes around the table in it, not through", () => {
    const { nodes, edges } = layoutDiagram(sightings());

    expect(edges).toHaveLength(3);
    for (const edge of edges) {
      for (const link of edge.links) {
        for (const node of nodes) {
          if (node.name === edge.from || node.name === edge.to) continue;
          const inside = pointsAlong(linkPath(link)).filter(
            ({ x, y }) => x > node.x && x < node.x + node.width && y > node.y && y < node.y + node.height,
          );
          expect(inside, `${edge.name} passes through ${node.name}`).toEqual([]);
        }
      }
    }
  });

  test("an arrow between neighbouring ranks is one curve", () => {
    const { edges } = layoutDiagram(sightings());
    const [link] = edges.find((e) => e.name === "suspects_room_id_fkey")!.links;

    expect(linkPath(link).match(/C/g)).toHaveLength(1);
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

  // Quoted identifiers may contain a dot.
  test("keeps apart two keys whose table and key names join to the same text", () => {
    const { edges } = layoutDiagram({
      truncated: false,
      tables: [
        table("a.b", ["x"], { foreign_keys: [{ name: "c", columns: ["x"], referenced_table: "a", referenced_columns: ["x"] }] }),
        table("a", ["x"], { foreign_keys: [{ name: "b.c", columns: ["x"], referenced_table: "a.b", referenced_columns: ["x"] }] }),
      ],
    });

    expect(new Set(edges.map((e) => e.id)).size).toBe(2);
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

  describe("on typical contest schemas", () => {
    test.each([
      ["a chain", chain],
      ["a star", star],
      ["a snowflake", snowflake],
      ["the e2e features game", sightings],
    ])("%s is drawn without overlaps or an arrow through a table", (_, schema) => {
      const diagram = layoutDiagram(schema());

      expectNoOverlaps(diagram.nodes);
      expectNoArrowThroughATable(diagram);
    });

    // Six tables in one row fit a window only at a fraction of their size.
    test("a chain folds into rows rather than one long row", () => {
      const { width, height } = layoutDiagram(chain());

      expect(width / height).toBeLessThan(3);
    });

    test("a star puts its dimensions on both sides of the fact, its arrows not crossing", () => {
      const diagram = layoutDiagram(star());
      const sales = byName(diagram.nodes, "sales");
      const others = diagram.nodes.filter((n) => n !== sales);

      expect(others.filter((n) => n.x + n.width <= sales.x).length).toBeGreaterThanOrEqual(3);
      expect(others.filter((n) => n.x >= sales.x + sales.width).length).toBeGreaterThanOrEqual(3);
      expect(diagram.width / diagram.height).toBeGreaterThan(1);
      expect(crossings(diagram)).toBe(0);
    });

    test("a snowflake's arrows cross at most once", () => {
      expect(crossings(layoutDiagram(snowflake()))).toBeLessThanOrEqual(1);
    });

    // A narrower table between wider ones: the arrow ends at its side, not
    // past the column and back.
    test("an arrow stays between the two sides it joins", () => {
      const { edges } = layoutDiagram(star());

      for (const link of edges.flatMap((e) => e.links)) {
        const [left, right] = [Math.min(link.x1, link.x2), Math.max(link.x1, link.x2)];
        for (const { x } of pointsAlong(linkPath(link))) {
          expect(x).toBeGreaterThanOrEqual(left - 0.5);
          expect(x).toBeLessThanOrEqual(right + 0.5);
        }
      }
    });

    // A game without keys is common: its tables are a block, not one long shelf.
    test("tables no key joins fill a landscape block", () => {
      const { nodes, width, height } = layoutDiagram(
        schemaOf(Array.from({ length: 12 }, (_, i) => [`t${i}`, ["name", "created_at", "note"]])),
      );

      expectNoOverlaps(nodes);
      expect(width / height).toBeGreaterThan(1);
      expect(width / height).toBeLessThan(2.5);
    });
  });

  test("routes the arrows again for tables moved elsewhere", () => {
    const { nodes, edges } = layoutDiagram(hotel());
    const moved = nodes.map((n) => (n.name === "rooms" ? { ...n, x: n.x + 1000, y: n.y + 400 } : n));

    const [link] = routeEdges(edges, moved).find((e) => e.name === "rooms_on_a_floor")!.links;
    const rooms = byName(moved, "rooms");
    const floors = byName(moved, "floors");
    expect(link.x1).toBe(rooms.x);
    expect(link.y1).toBe(rowMiddle(rooms, "lvl"));
    expect(link.x2).toBe(floors.x + floors.width);
    expect(link.y2).toBe(rowMiddle(floors, "level"));
  });

  // Tables a participant placed nearly one above the other, a third between.
  test("an arrow between tables moved almost into one column stays near them", () => {
    const { nodes, edges } = layoutDiagram(hotel());
    const at = (name: string, x: number, y: number) => ({ ...byName(nodes, name), x, y });
    // floors ends four units left of where rooms starts; staff spans that gap.
    const floors = byName(nodes, "floors");
    const moved = [at("rooms", 953, 98), at("staff", 945, 270), at("floors", 949 - floors.width, 416)];

    const [link] = routeEdges(edges, moved).find((e) => e.name === "rooms_on_a_floor")!.links;
    for (const { x, y } of pointsAlong(linkPath(link))) {
      expect(y).toBeGreaterThan(0);
      expect(y).toBeLessThan(700);
      expect(x).toBeGreaterThan(600);
      expect(x).toBeLessThan(1300);
    }
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // The API's bound is 200 tables. The layout's time was measured and is
  // recorded beside its budget; here only which path ran is asserted, since a
  // wall clock flakes on a loaded runner.
  test("lays out 200 tables with dagre, without overlaps", () => {
    const layout = vi.spyOn(dagre, "layout");
    const schema = many(200, (i) =>
      i === 0
        ? []
        : [
            { to: `t${(i * 31 + 7) % i}`, column: "c0" },
            { to: `t${Math.floor(i / 2)}`, column: "c1" },
          ],
    );

    const { nodes, edges } = layoutDiagram(schema);

    expect(layout).toHaveBeenCalledTimes(1);
    expect(nodes).toHaveLength(200);
    expect(edges).toHaveLength(398);
    expectNoOverlaps(nodes);
  });

  // Left to right, a rank is a column; 120 tables in one rank fit the window
  // only as specks.
  test("wraps a star of tables pointing at one into columns", () => {
    const { nodes, width, height } = layoutDiagram(many(121, (i) => (i === 0 ? [] : [{ to: "t0", column: "c0" }])));

    expect(height).toBeLessThan(width * 3);
    expectNoOverlaps(nodes);
  });

  // Within the API's 1000 keys, yet about a second in dagre's cheaper mode.
  test("past dagre's budget, ranks the tables itself, the arrows running left to right", () => {
    const layout = vi.spyOn(dagre, "layout");
    const schema = many(105, (i) =>
      // Ten tables in a chain closed into a cycle, and every other table
      // pointing at all ten: long edges across ranks are what make dagre slow.
      i === 0
        ? [{ to: "t9", column: "c0" }]
        : Array.from({ length: Math.min(i, 10) }, (_, k) => ({ to: `t${k}`, column: `c${k}` })),
    );

    const { nodes, edges } = layoutDiagram(schema);

    expect(layout).not.toHaveBeenCalled();
    expect(edges).toHaveLength(1 + 45 + 95 * 10);
    expectNoOverlaps(nodes);
    // Breaking the cycle at t0 turns the keys into it backwards; every other
    // arrow runs left to right.
    for (const edge of edges.filter((e) => e.from !== "t0" && e.to !== "t0")) {
      expect(edge.links.every((l) => l.out1 === 1 && l.out2 === -1 && l.x2 > l.x1), edge.id).toBe(true);
    }
  });
});
