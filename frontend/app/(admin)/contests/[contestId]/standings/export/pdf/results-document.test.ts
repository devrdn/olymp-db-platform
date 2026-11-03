import type { Content } from "pdfmake";
import { describe, expect, test } from "vitest";

import type { ResultsReport, ResultsRow } from "@/lib/api/results";
import en from "@/lib/i18n/dictionaries/en";

import { A4, MM } from "../../../story/pdf/theme";
import {
  contestTitle,
  offsetLabel,
  pdfMoment,
  resultsProtocolDocument,
  resultsTableDocument,
  type ResultsPdfInput,
} from "./results-document";

const texts = en.leaderboard.export.pdf;

function row(id: string, over: Partial<ResultsRow> = {}): ResultsRow {
  return {
    id,
    place: null,
    prize: null,
    fullName: undefined,
    login: undefined,
    email: undefined,
    deleted: false,
    disqualified: false,
    winner: false,
    solved: 0,
    penalty: null,
    points: null,
    lastSolvedAt: null,
    bonusCredit: null,
    cells: [],
    ...over,
  };
}

const report: ResultsReport = {
  contest: {
    id: "c0ffee00-0000-4000-8000-000000000001",
    titles: { ro: "Olimpiada de baze de date, ediția a cincea", en: "Database olympiad, fifth edition" },
    defaultLanguage: "ro",
    status: "finished",
    scoring: "icpc",
    icpcPenaltyMin: 20,
    prizePlaces: { first: 1, second: 1, third: 1 },
    startsAt: "2026-11-02T08:00:00Z",
    endsAt: "2026-11-02T12:00:00Z",
  },
  status: "final",
  hiddenByFreeze: false,
  generatedAt: "2026-11-02T12:10:00Z",
  registered: 12,
  detail: "summary",
  fields: ["full_name", "login"],
  bonusCredit: false,
  questions: [
    { position: 1, letter: "A", bonus: false, titles: {} },
    { position: 2, letter: "B", bonus: false, titles: {} },
  ],
  rows: [
    row("r1", {
      place: 1,
      prize: 1,
      fullName: "Ștefan Țurcanu",
      login: "sturcanu",
      solved: 2,
      penalty: 75,
      cells: [
        { solved: true, minute: 15, wrong: 0 },
        { solved: true, minute: 40, wrong: 1 },
      ],
    }),
    row("r2", {
      place: 2,
      prize: 2,
      fullName: "Иван Петров",
      login: "ipetrov",
      solved: 1,
      penalty: 50,
      cells: [
        { solved: true, minute: 50, wrong: 0 },
        { solved: false, wrong: 3 },
      ],
    }),
    row("r3", {
      place: 3,
      prize: 3,
      deleted: true,
      solved: 1,
      penalty: 90,
      cells: [
        { solved: false, wrong: 0 },
        { solved: true, minute: 90, wrong: 0 },
      ],
    }),
  ],
  fingerprint: "a1b2c3d4",
  mayExportEmail: false,
};

const input: ResultsPdfInput = {
  report,
  language: "en",
  texts,
  logo: "mark",
  installationName: "Universitatea de Stat din Moldova",
  timeZone: "Europe/Chisinau",
};

/** The generation time as the PDF prints it in Chisinau, two hours ahead of UTC in November. */
const GENERATED = "02.11.2026 14:10 (UTC+2)";

type PageFunction = (page: number, pages: number, size: typeof A4) => Content;

/** Every string a definition's `text` nodes print, depth first, with the header and footer of page 1 of 2. */
function strings(node: unknown): string[] {
  if (Array.isArray(node)) return node.flatMap(strings);
  if (typeof node !== "object" || node === null) return [];
  return Object.entries(node).flatMap(([key, value]) => {
    if (key === "text" && typeof value === "string") return [value];
    if ((key === "header" || key === "footer") && typeof value === "function") {
      return strings((value as PageFunction)(1, 2, A4));
    }
    return strings(value);
  });
}

/** All the printed text of a definition, run together. */
function printed(definition: Content): string {
  return strings(definition).join(" ");
}

/** Every node of a definition that `match` accepts, depth first. */
function nodesWhere(node: unknown, match: (node: Content) => boolean): Content[] {
  if (Array.isArray(node)) return node.flatMap((child) => nodesWhere(child, match));
  if (typeof node !== "object" || node === null) return [];
  const own = match(node as Content) ? [node as Content] : [];
  return [...own, ...Object.values(node).flatMap((child) => nodesWhere(child, match))];
}

/** The results table: the one table with a header row. */
function resultsTable(definition: Content): { header: string[]; body: string[][] } {
  const [node] = nodesWhere(definition.content, (candidate) => {
    const table = candidate.table as { headerRows?: number } | undefined;
    return table?.headerRows === 1;
  });
  const [header, ...body] = (node.table as { body: unknown[][] }).body.map((cells) =>
    cells.map((cell) => strings(cell).join(" ")),
  );
  return { header, body };
}

/** The top-level block of the content that prints `text`. */
function blockWith(definition: Content, text: string): Content {
  const block = (definition.content as Content[]).find((candidate) => printed(candidate).includes(text));
  if (!block) throw new Error(`no block prints ${text}`);
  return block;
}

const upper = (label: string) => label.toLocaleUpperCase("en");

describe("resultsTableDocument", () => {
  test("a summary table is portrait and a detailed one landscape", () => {
    const summary = resultsTableDocument(input);
    const detailed = resultsTableDocument({ ...input, report: { ...report, detail: "detailed" } });
    const rule = (definition: Content) => {
      const [line] = nodesWhere((definition.header as PageFunction)(1, 1, A4), (node) => node.type === "line");
      return line.x2 as number;
    };

    expect(summary.pageSize).toBe("A4");
    expect(summary.pageOrientation ?? "portrait").toBe("portrait");
    expect(rule(summary)).toBeCloseTo(A4.width - 40 * MM, 6);
    expect(detailed.pageOrientation).toBe("landscape");
    expect(rule(detailed)).toBeCloseTo(A4.height - 40 * MM, 6);
    expect(summary.pageMargins).toEqual(detailed.pageMargins);
    expect((summary.pageMargins as number[])[0]).toBeCloseTo(20 * MM, 6);
  });

  test("an ICPC table heads question columns by letter and a points table by number", () => {
    const icpc = resultsTable(resultsTableDocument({ ...input, report: { ...report, detail: "detailed" } }));
    const points = resultsTable(
      resultsTableDocument({
        ...input,
        report: {
          ...report,
          detail: "detailed",
          contest: { ...report.contest, scoring: "points" },
          questions: report.questions.map((question) => ({ ...question, letter: undefined })),
          rows: report.rows.map((each) => ({
            ...each,
            penalty: null,
            points: 10,
            cells: [
              { solved: true, points: 10, attempts: 1 },
              { solved: false, points: 0, attempts: 2 },
            ],
          })),
        },
      }),
    );

    expect(icpc.header).toEqual(expect.arrayContaining(["A", "B", upper(texts.columns.penalty)]));
    expect(icpc.header).not.toContain("1");
    expect(points.header).toEqual(expect.arrayContaining(["1", "2", upper(texts.columns.points)]));
    expect(points.header).not.toContain("A");
    // A solve after one wrong attempt, at minute 40; three wrong attempts and no solve.
    expect(icpc.body[0]).toContain("+1 40");
    expect(icpc.body[1]).toContain("−3");
  });

  test("the table shows only the identity columns the report carries", () => {
    const onlyLogin = resultsTable(resultsTableDocument({ ...input, report: { ...report, fields: ["login"] } }));
    const all = resultsTable(
      resultsTableDocument({
        ...input,
        report: { ...report, fields: ["full_name", "login", "email"], rows: [row("r1", { email: "s@example.md" })] },
      }),
    );

    expect(onlyLogin.header).toContain(upper(texts.columns.login));
    expect(onlyLogin.header).not.toContain(upper(texts.columns.fullName));
    expect(onlyLogin.header).not.toContain(upper(texts.columns.email));
    expect(onlyLogin.body[0]).toContain("sturcanu");
    expect(onlyLogin.body.flat().join(" ")).not.toContain("Ștefan");
    expect(all.header).toEqual(
      expect.arrayContaining([upper(texts.columns.fullName), upper(texts.columns.login), upper(texts.columns.email)]),
    );
    expect(all.body[0]).toContain("s@example.md");
  });

  test("a preliminary table carries the preliminary band with the generation time", () => {
    const preliminary = printed(resultsTableDocument({ ...input, report: { ...report, status: "preliminary" } }));
    const final = printed(resultsTableDocument(input));

    expect(preliminary).toContain(`Preliminary results, generated at ${GENERATED} — may change`);
    expect(final).not.toContain("Preliminary");
  });

  test("a table holding results hidden by the freeze says so", () => {
    expect(printed(resultsTableDocument({ ...input, report: { ...report, hiddenByFreeze: true } }))).toContain(
      "Contains results not yet shown to participants",
    );
    expect(printed(resultsTableDocument(input))).not.toContain("not yet shown");
  });

  test("the title, the contest's dates and the page numbers", () => {
    const definition = resultsTableDocument(input);
    const text = printed(definition);

    expect(text).toContain("Results");
    expect(text).toContain("02.11.2026 10:00 (UTC+2)");
    expect(text).toContain("02.11.2026 14:00 (UTC+2)");
    expect(strings((definition.footer as PageFunction)(1, 2, A4))).toEqual(["1 / 2"]);
  });

  test("a disqualified row is marked and holds no place", () => {
    const { body } = resultsTable(
      resultsTableDocument({
        ...input,
        report: { ...report, rows: [row("r9", { fullName: "Ana Rusu", login: "arusu", disqualified: true })] },
      }),
    );

    expect(body[0][0]).toBe("—");
    expect(body[0].join(" ")).toContain(texts.disqualified);
  });
});

describe("resultsProtocolDocument", () => {
  test("the protocol refuses a preliminary report", () => {
    expect(() => resultsProtocolDocument({ ...input, report: { ...report, status: "preliminary" } })).toThrow();
  });

  test("a final protocol says the results may change until the archive; an archived one does not", () => {
    const final = printed(resultsProtocolDocument(input));
    const archived = printed(resultsProtocolDocument({ ...input, report: { ...report, status: "archived" } }));

    expect(final).toContain(`Results as of ${GENERATED}. They may change until the contest is archived.`);
    expect(archived).not.toContain("They may change");
  });

  test("a protocol holding results hidden by the freeze says so", () => {
    expect(printed(resultsProtocolDocument({ ...input, report: { ...report, hiddenByFreeze: true } }))).toContain(
      "Contains results not yet shown to participants",
    );
  });

  test("the protocol states the rules, the participant count and the prize places", () => {
    const text = printed(resultsProtocolDocument(input));

    expect(text).toContain("Universitatea de Stat din Moldova");
    expect(text).toContain("Results protocol");
    expect(text).toContain("Participants: 3 of 12");
    expect(text).toContain(texts.scoring.icpc);
    expect(text).toContain(texts.icpcPenalty.other.replace("{n}", "20"));
    expect(text).toContain(texts.prizePlaces.replace("{first}", "1").replace("{second}", "1").replace("{third}", "1"));
  });

  test("the rules of a points contest say nothing of an ICPC penalty", () => {
    const text = printed(
      resultsProtocolDocument({ ...input, report: { ...report, contest: { ...report.contest, scoring: "points" } } }),
    );

    expect(text).toContain(texts.scoring.points);
    expect(text).not.toContain(texts.icpcPenalty.other.replace("{n}", "20"));
  });

  test("the protocol lists prize winners by prize with names only", () => {
    const sequence = strings(blockWith(resultsProtocolDocument(input), texts.winners));

    expect(sequence).toEqual([
      texts.winners,
      texts.prizes.first,
      "Ștefan Țurcanu",
      texts.prizes.second,
      "Иван Петров",
      texts.prizes.third,
      texts.deleted,
    ]);
  });

  test("a winner without a carried name is listed by login", () => {
    const sequence = strings(
      blockWith(resultsProtocolDocument({ ...input, report: { ...report, fields: ["login"], rows: [row("r1", { place: 1, prize: 1, login: "sturcanu" })] } }), texts.winners),
    );

    expect(sequence).toEqual([texts.winners, texts.prizes.first, "sturcanu"]);
  });

  test("a live winner whose carried fields are empty is a dash, not a deleted account", () => {
    const sequence = strings(
      blockWith(resultsProtocolDocument({ ...input, report: { ...report, fields: ["email"], rows: [row("r1", { place: 1, prize: 1 })] } }), texts.winners),
    );

    expect(sequence).toEqual([texts.winners, texts.prizes.first, "—"]);
  });

  test("the protocol prints the jury names typed on the page and a line for each", () => {
    const definition = resultsProtocolDocument({
      ...input,
      jury: { chair: "Ana Popescu", members: ["Ion Rusu", "Мария Ковальчук"] },
    });
    const jury = blockWith(definition, texts.chair);
    const text = printed(jury);
    const lines = nodesWhere(jury, (node) => node.type === "line");

    expect(text).toContain(texts.chair);
    expect(text).toContain("Ana Popescu");
    expect(text).toContain(texts.members);
    expect(text).toContain("Ion Rusu");
    expect(text).toContain("Мария Ковальчук");
    expect(text).toContain(texts.date);
    // The chair, two members and the date.
    expect(lines).toHaveLength(4);
  });

  test("with no names typed, the jury keeps a line to sign for the chair and one for a member", () => {
    const jury = blockWith(resultsProtocolDocument(input), texts.chair);

    expect(nodesWhere(jury, (node) => node.type === "line")).toHaveLength(3);
  });

  test("a heading stays on its page with the first line it heads, and a long jury list is never held on one page", () => {
    const members = Array.from({ length: 40 }, (_, index) => `Member ${index + 1}`);
    const jury = blockWith(resultsProtocolDocument({ ...input, jury: { chair: "Ana Popescu", members } }), texts.chair);
    const held = nodesWhere(jury, (node) => node.unbreakable === true && Array.isArray(node.stack));
    const heads = held.map((node) => strings(node).slice(0, 2));

    // The library cuts an unbreakable block taller than a page to its first page.
    expect(jury.unbreakable).toBeFalsy();
    expect(heads).toEqual(
      expect.arrayContaining([
        [texts.chair, "Ana Popescu"],
        [texts.members, "Member 1"],
      ]),
    );
    expect(strings(jury)).toEqual(expect.arrayContaining(members));
  });

  test("the protocol footer carries the generation time and the fingerprint", () => {
    const definition = resultsProtocolDocument(input);
    const footer = strings((definition.footer as PageFunction)(1, 2, A4)).join(" ");

    expect(footer).toContain("1 / 2");
    expect(footer).toContain(`Generated at ${GENERATED}`);
    expect(footer).toContain("a1b2c3d4");
  });

  test("the protocol's sections come in order", () => {
    const text = printed(resultsProtocolDocument(input));
    const order = ["Results protocol", texts.rules, upper(texts.columns.place), texts.winners, texts.chair];

    const positions = order.map((part) => text.indexOf(part));
    expect(positions.every((position) => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((a, b) => a - b));
  });
});

describe("contestTitle", () => {
  test("the contest title falls back to the default language", () => {
    expect(contestTitle(report, "en")).toBe("Database olympiad, fifth edition");
    expect(contestTitle(report, "ru")).toBe("Olimpiada de baze de date, ediția a cincea");
    expect(contestTitle({ ...report, contest: { ...report.contest, titles: {} } }, "ru")).toBe("");
    expect(printed(resultsTableDocument({ ...input, language: "ru" }))).toContain(
      "Olimpiada de baze de date, ediția a cincea",
    );
  });
});

describe("pdfMoment", () => {
  test("pdfMoment writes the browser's zone as an offset", () => {
    expect(pdfMoment("2026-11-02T12:10:00Z", "Europe/Chisinau")).toBe("02.11.2026 14:10 (UTC+2)");
    expect(pdfMoment("2026-07-01T09:05:00Z", "Europe/Chisinau")).toBe("01.07.2026 12:05 (UTC+3)");
    expect(pdfMoment("2026-11-02T12:10:00Z", "Asia/Kolkata")).toBe("02.11.2026 17:40 (UTC+5:30)");
    expect(pdfMoment("2026-11-02T12:10:00Z", "UTC")).toBe("02.11.2026 12:10 (UTC)");
    expect(pdfMoment("2026-11-02T23:30:00Z", "America/St_Johns")).toBe("02.11.2026 20:00 (UTC-3:30)");
  });
});

describe("offsetLabel", () => {
  test("offsetLabel names a zero offset UTC however the engine writes it", () => {
    expect(offsetLabel("GMT")).toBe("UTC");
    expect(offsetLabel("GMT+00:00")).toBe("UTC");
    expect(offsetLabel("GMT-00:00")).toBe("UTC");
  });

  test("offsetLabel drops zero minutes and keeps the others", () => {
    expect(offsetLabel("GMT+02:00")).toBe("UTC+2");
    expect(offsetLabel("GMT+05:30")).toBe("UTC+5:30");
    expect(offsetLabel("GMT\u221203:30")).toBe("UTC-3:30");
  });
});
