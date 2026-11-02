// @vitest-environment node
import { join } from "node:path";
import type { Content } from "pdfmake";
import { describe, expect, test, vi } from "vitest";

import type { ResultsReport, ResultsRow } from "@/lib/api/results";
import ro from "@/lib/i18n/dictionaries/ro";
import ru from "@/lib/i18n/dictionaries/ru";

import { preparedLibrary, type StoryPdfOptions } from "../../../story/pdf/make-story-pdf";
import { embeddedFaces, missingGlyphs, namedFaces, readFromDisk } from "../../../story/pdf/pdf-test-support";
import { A4 } from "../../../story/pdf/theme";
import { makeResultsPdf, type ResultsPdfRequest } from "./make-results-pdf";

const FONTS = join(process.cwd(), "public/fonts/pdf");

const options = (overrides: Partial<StoryPdfOptions> = {}): StoryPdfOptions => ({
  fontSource: FONTS,
  readFont: readFromDisk,
  drawLogo: async () => Promise.reject(new Error("no canvas in a test")),
  ...overrides,
});

const TEXTS = { ro: ro.leaderboard.export.pdf, ru: ru.leaderboard.export.pdf };

function row(id: string, over: Partial<ResultsRow>): ResultsRow {
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

/** An ICPC report naming people in both scripts, with a deleted and a disqualified row and every band. */
function icpcReport(status: ResultsReport["status"], detail: ResultsReport["detail"]): ResultsReport {
  return {
    contest: {
      id: "c0ffee00-0000-4000-8000-000000000001",
      titles: { ro: "Olimpiada „Știință și țară”", ru: "Олимпиада «Щит и меч»: ёлка" },
      defaultLanguage: "ro",
      status: status === "archived" ? "archived" : status === "final" ? "finished" : "running",
      scoring: "icpc",
      icpcPenaltyMin: 20,
      prizePlaces: { first: 1, second: 1, third: 1 },
      startsAt: "2026-11-02T08:00:00Z",
      endsAt: "2026-11-02T12:00:00Z",
    },
    status,
    hiddenByFreeze: true,
    generatedAt: "2026-11-02T12:10:00Z",
    registered: 12,
    detail,
    fields: ["full_name", "login", "email"],
    bonusCredit: false,
    questions: ["A", "B", "C"].map((letter, index) => ({ position: index + 1, letter, bonus: false, titles: {} })),
    rows: [
      row("r1", {
        place: 1,
        prize: 1,
        fullName: "Ștefan Țurcanu",
        login: "sturcanu",
        email: "ștefan@usm.md",
        solved: 2,
        penalty: 75,
        lastSolvedAt: "2026-11-02T08:40:00Z",
        cells: [
          { solved: true, minute: 15, wrong: 0 },
          { solved: true, minute: 40, wrong: 1 },
          { solved: false, wrong: 4 },
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
          { solved: false, wrong: 0 },
        ],
      }),
      row("r3", { place: 3, prize: 3, deleted: true, solved: 1, penalty: 90 }),
      row("r4", { fullName: "Țânțăreanu Șerban", login: "tserban", disqualified: true, solved: 3, penalty: 10 }),
    ],
    fingerprint: "a1b2c3d4",
    mayExportEmail: true,
  };
}

function request(language: "ro" | "ru", report: ResultsReport): ResultsPdfRequest {
  return {
    report,
    language,
    texts: TEXTS[language],
    installationName: "Universitatea de Stat din Moldova — Университет",
    timeZone: "Europe/Chisinau",
    jury: { chair: "Ana Popescu", members: ["Ion Țurcan", "Мария Ковальчук"] },
  };
}

async function bytes(blob: Blob): Promise<Buffer> {
  return Buffer.from(await blob.arrayBuffer());
}

describe("makeResultsPdf", () => {
  test("a Romanian and a Russian table and protocol draw every letter", async () => {
    const languages: ("ro" | "ru")[] = ["ro", "ru"];
    for (const language of languages) {
      for (const detail of ["summary", "detailed"] as const) {
        const table = await bytes(await makeResultsPdf("table", request(language, icpcReport("preliminary", detail)), options()));
        const protocol = await bytes(await makeResultsPdf("protocol", request(language, icpcReport("final", detail)), options()));

        expect({ language, detail, kind: "table", missing: missingGlyphs(table) }).toEqual({ language, detail, kind: "table", missing: 0 });
        expect({ language, detail, kind: "protocol", missing: missingGlyphs(protocol) }).toEqual({
          language,
          detail,
          kind: "protocol",
          missing: 0,
        });
      }
    }
  });

  test("a points table with a bonus column draws every letter too", async () => {
    const icpc = icpcReport("archived", "detailed");
    const report: ResultsReport = {
      ...icpc,
      contest: { ...icpc.contest, scoring: "points" },
      bonusCredit: true,
      questions: icpc.questions.map((question, index) => ({ ...question, letter: undefined, bonus: index === 2 })),
      rows: icpc.rows.map((each) => ({
        ...each,
        penalty: null,
        points: 12,
        bonusCredit: 2,
        cells: [
          { solved: true, points: 10, attempts: 1 },
          { solved: false, points: 0, attempts: 2 },
          { solved: true, attempts: 1 },
        ],
      })),
    };
    for (const language of ["ro", "ru"] as const) {
      for (const kind of ["table", "protocol"] as const) {
        const pdf = await bytes(await makeResultsPdf(kind, request(language, report), options()));
        expect({ language, kind, missing: missingGlyphs(pdf) }).toEqual({ language, kind, missing: 0 });
      }
    }
  });

  test("the PDF embeds only the three faces", async () => {
    const pdf = await bytes(await makeResultsPdf("protocol", request("ro", icpcReport("final", "detailed")), options()));

    expect(pdf.subarray(0, 5).toString("latin1")).toBe("%PDF-");
    for (const face of embeddedFaces(pdf)) expect(face).toMatch(/^(Onest|Literata|JetBrainsMono)-/);
    for (const face of namedFaces(pdf)) expect(face).toMatch(/^[A-Z]{6}\+(Onest|Literata|JetBrainsMono)-/);
  });

  test("the protocol of a report still moving is refused before anything is drawn", async () => {
    await expect(makeResultsPdf("protocol", request("ro", icpcReport("preliminary", "summary")), options())).rejects.toThrow();
  });

  test("a logo that cannot be drawn is left out and the file is still made", async () => {
    const drawLogo = vi.fn(async () => Promise.reject(new Error("broken")));

    const blob = await makeResultsPdf(
      "table",
      { ...request("ro", icpcReport("final", "summary")), logoHref: "/api/v1/settings/images/logo?v=1" },
      options({ drawLogo }),
    );

    expect(drawLogo).toHaveBeenCalledWith("/api/v1/settings/images/logo?v=1");
    expect(blob.type).toBe("application/pdf");
    expect((await bytes(blob)).toString("latin1")).not.toMatch(/\/Subtype\s*\/Image/);
  });

  test("the header cuts a long contest title to its line, and a landscape page gives it more room", async () => {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);
    const createPdf = vi.spyOn(pdfMake, "createPdf");
    const long = "Olimpiada republicană de baze de date „Știință și țară”, ediția a cincea, ".repeat(4);
    const titled = (detail: ResultsReport["detail"]) => {
      const report = icpcReport("final", detail);
      return { ...report, contest: { ...report.contest, titles: { ro: long } } };
    };
    const headerTitle = async (detail: ResultsReport["detail"]) => {
      await makeResultsPdf("table", request("ro", titled(detail)), options());
      const definition = createPdf.mock.calls.at(-1)![0];
      const header = (definition.header as (page: number, pages: number, size: typeof A4) => Content)(1, 1, A4);
      const [line] = header.stack as Content[];
      const [, cell] = line.columns as Content[];
      return cell.text as string;
    };

    try {
      const portrait = await headerTitle("summary");
      const landscape = await headerTitle("detailed");

      expect(portrait).toMatch(/…$/);
      expect(landscape).toMatch(/…$/);
      expect(landscape.length).toBeGreaterThan(portrait.length);
    } finally {
      createPdf.mockRestore();
    }
  });
});

