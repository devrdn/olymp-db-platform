/**
 * The results table and the results protocol as pdfmake document definitions,
 * drawn from the JSON report in the story PDF's theme. Nothing here loads the
 * library; `make-results-pdf.ts` does, on demand.
 */

import type { Content } from "pdfmake";

import type { ResultsCell, ResultsField, ResultsReport, ResultsRow } from "@/lib/api/results";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { plural } from "@/lib/i18n/plural";

import type { LogoImage } from "../../../story/pdf/logo";
import { runningHeader } from "../../../story/pdf/story-document";
import {
  A4,
  ASCENDER,
  COLOR,
  DATA_SIZE,
  MEASURE,
  NATURAL,
  PAGE,
  PX,
  SPACE,
  STEP,
  inset,
  lineHeight,
  type Step,
} from "../../../story/pdf/theme";

export type ResultsPdfTexts = Dictionary["leaderboard"]["export"]["pdf"];

export type Jury = { chair: string; members: string[] };

export type ResultsPdfInput = {
  report: ResultsReport;
  /** The language the PDF is printed in; `texts` are that language's. */
  language: Locale;
  texts: ResultsPdfTexts;
  /** The uploaded logo, drawn; `"mark"` for the product mark; `null` for none. */
  logo: LogoImage | "mark" | null;
  installationName: string;
  /** The IANA zone every time is printed in, the organiser's browser's. */
  timeZone: string;
  /** The contest title as the header prints it, cut to its line; the whole title when absent. */
  headerTitle?: string;
  jury?: Jury;
};

/** The text column of a landscape page: the sheet's long side less the same margins. */
const LANDSCAPE_MEASURE = A4.height - 2 * PAGE.side;

/** The text column a report's PDF is laid out on: a detailed report needs the landscape page. */
export function measureOf(report: ResultsReport): number {
  return report.detail === "detailed" ? LANDSCAPE_MEASURE : MEASURE;
}

/** `02.11.2026 14:10 (UTC+2)`: the moment in `timeZone`, with that zone's offset at the moment. */
export function pdfMoment(iso: string, timeZone: string): string {
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat("en-GB", {
      timeZone,
      day: "2-digit",
      month: "2-digit",
      year: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
      timeZoneName: "longOffset",
    })
      .formatToParts(new Date(iso))
      .map((part) => [part.type, part.value]),
  );
  return `${parts.day}.${parts.month}.${parts.year} ${parts.hour}:${parts.minute} (${offsetLabel(parts.timeZoneName ?? "")})`;
}

/**
 * `UTC+2`, `UTC+5:30`, `UTC-3:30` or `UTC` from a `longOffset` zone name. Engines differ at zero
 * (a bare "GMT" on older ICU, "GMT+00:00" on newer) and some write the minus as U+2212.
 */
export function offsetLabel(longOffset: string): string {
  const offset = /GMT([+\-−])(\d{2}):(\d{2})/.exec(longOffset);
  if (!offset || (offset[2] === "00" && offset[3] === "00")) return "UTC";
  const sign = offset[1] === "+" ? "+" : "-";
  const minutes = offset[3] === "00" ? "" : `:${offset[3]}`;
  return `UTC${sign}${Number(offset[2])}${minutes}`;
}

/** The contest's title in `language`, else in its default language, else nothing. */
export function contestTitle(report: ResultsReport, language: string): string {
  const { titles, defaultLanguage } = report.contest;
  return titles[language] || titles[defaultLanguage] || "";
}

function fill(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (whole, key: string) => (key in values ? String(values[key]) : whole));
}

function textStyle(step: Step, color: string = COLOR.ink): Content {
  return {
    font: step.face,
    fontSize: step.size,
    lineHeight: lineHeight(step),
    characterSpacing: step.tracking * step.size,
    bold: step.medium,
    color,
  };
}

function heading(text: string, step: Step, marginTop: number): Content {
  return { text, ...textStyle(step), margin: [0, marginTop + inset(step), 0, SPACE.item] };
}

/**
 * A heading kept on one page with the first thing it heads, so no page ends on
 * a heading. Only that first item is held, never the whole list: the library
 * cuts an unbreakable block taller than a page to its first page.
 */
function headed(title: Content, items: Content[]): Content[] {
  if (items.length === 0) return [title];
  const [first, ...rest] = items;
  return [{ unbreakable: true, stack: [title, first] }, ...rest];
}

function paragraph(text: string, color: string = COLOR.ink, marginTop: number = SPACE.item): Content {
  return { text, ...textStyle(STEP.body, color), margin: [0, marginTop, 0, 0] };
}

/** A notice under the title: a sunk band with the accent's rule on its left. */
function band(text: string): Content {
  return {
    table: { widths: ["*"], body: [[{ text, ...textStyle(STEP.body), fillColor: COLOR.sunk }]] },
    layout: {
      hLineWidth: () => 0,
      vLineWidth: (i: number) => (i === 0 ? SPACE.quoteRule : 0),
      vLineColor: () => COLOR.accent,
      paddingLeft: () => SPACE.quoteIndent,
      paddingRight: () => SPACE.quoteIndent,
      paddingTop: () => SPACE.cellY,
      paddingBottom: () => SPACE.cellY,
    },
    margin: [0, SPACE.block, 0, 0],
  };
}

function datesLine({ report, texts, timeZone }: ResultsPdfInput): Content[] {
  const { startsAt, endsAt } = report.contest;
  const start = startsAt ? pdfMoment(startsAt, timeZone) : null;
  const end = endsAt ? pdfMoment(endsAt, timeZone) : null;
  const line =
    start && end
      ? fill(texts.dates, { start, end })
      : start
        ? fill(texts.startsOnly, { start })
        : end
          ? fill(texts.endsOnly, { end })
          : null;
  return line ? [paragraph(line, COLOR.ink2)] : [];
}

/** The document's own title in the `text-h2` step, with the contest's title under it. */
function titleBlock(title: string, input: ResultsPdfInput): Content[] {
  const step = STEP.h2;
  return [
    { text: title, ...textStyle(step), margin: [0, inset(step), 0, 0] },
    { text: contestTitle(input.report, input.language), ...textStyle(STEP.h3), margin: [0, SPACE.block, 0, 0] },
  ];
}

const PRIZE_MARK: Record<1 | 2 | 3, string> = { 1: "I", 2: "II", 3: "III" };

/** The name the protocol lists a winner by: full name, else login, else email; with none, the deleted label for a deleted account and a dash for a live one. */
function nameOf(row: ResultsRow, texts: ResultsPdfTexts): string {
  if (row.fullName) return row.fullName;
  if (row.login) return row.login;
  if (!row.deleted && row.email) return row.email;
  return row.deleted ? texts.deleted : "—";
}

const FIELD_VALUE: Record<ResultsField, (row: ResultsRow) => string | undefined> = {
  full_name: (row) => row.fullName,
  login: (row) => row.login,
  email: (row) => row.email,
};

const FIELD_LABEL: Record<ResultsField, keyof ResultsPdfTexts["columns"]> = {
  full_name: "fullName",
  login: "login",
  email: "email",
};

type Column = { label: string; width: string; align: "left" | "right" | "center"; cell: (row: ResultsRow) => Content };

/** The table's sizes: a landscape table carries more columns, so it sets its text a step smaller. */
function tableSteps(detailed: boolean): { text: Step; data: number } {
  return detailed ? { text: STEP.small, data: STEP.small.size * (DATA_SIZE / STEP.body.size) } : { text: STEP.body, data: DATA_SIZE };
}

function columnsOf(input: ResultsPdfInput): Column[] {
  const { report, texts, timeZone } = input;
  const detailed = report.detail === "detailed";
  const icpc = report.contest.scoring === "icpc";
  const steps = tableSteps(detailed);
  const text = (value: string, color: string = COLOR.ink): Content => ({ text: value, ...textStyle(steps.text, color) });
  const figure = figures(steps);
  const number = (value: number | null) => figure(value === null ? "" : String(value));

  const identity: Column[] = report.fields.map((field, index) => ({
    label: texts.columns[FIELD_LABEL[field]],
    width: field === "login" && report.fields.length > 1 ? "auto" : "*",
    align: "left",
    cell: (row) => {
      const value = FIELD_VALUE[field](row);
      const shown = value ? text(value) : row.deleted && index === 0 ? { ...text(texts.deleted, COLOR.ink3), italics: true } : text("");
      if (!(row.disqualified && index === 0)) return shown;
      return { stack: [shown, text(texts.disqualified, COLOR.ink2)] };
    },
  }));

  const questions: Column[] = detailed
    ? report.questions.map((question, index) => ({
        label: icpc && question.letter ? question.letter : String(question.position),
        width: "auto",
        align: "center",
        cell: (row) => questionCell(row.cells[index], icpc, figure),
      }))
    : [];

  const totals: Column[] = icpc
    ? [
        { label: texts.columns.solved, width: "auto", align: "right", cell: (row) => number(row.solved) },
        { label: texts.columns.penalty, width: "auto", align: "right", cell: (row) => number(row.penalty) },
      ]
    : [
        { label: texts.columns.points, width: "auto", align: "right", cell: (row) => number(row.points) },
        ...(report.bonusCredit
          ? [{ label: texts.columns.bonusCredit, width: "auto", align: "right" as const, cell: (row: ResultsRow) => number(row.bonusCredit) }]
          : []),
        { label: texts.columns.solved, width: "auto", align: "right", cell: (row) => number(row.solved) },
        {
          label: texts.columns.lastSolved,
          width: "auto",
          align: "right",
          cell: (row) => text(row.lastSolvedAt ? pdfMoment(row.lastSolvedAt, timeZone) : "", COLOR.ink2),
        },
      ];

  return [
    { label: texts.columns.place, width: "auto", align: "right", cell: (row) => figure(row.place === null ? "—" : String(row.place)) },
    { label: texts.columns.prize, width: "auto", align: "center", cell: (row) => figure(row.prize ? PRIZE_MARK[row.prize] : "") },
    ...identity,
    ...questions,
    ...totals,
  ];
}

/**
 * A question's cell, as the screen's grid draws it. ICPC: "+" or "+N" (wrong
 * attempts before the solve) over the minute, "−N" for wrong attempts without
 * a solve. The other modes: the points the question earned over the attempts;
 * a solved bonus question earns none by itself and shows "+".
 */
function questionCell(cell: ResultsCell | undefined, icpc: boolean, figure: Figure): Content {
  if (!cell) return figure("");
  if (icpc) {
    const wrong = cell.wrong ?? 0;
    if (cell.solved) {
      return { stack: [figure(wrong > 0 ? `+${wrong}` : "+", COLOR.accent), figure(String(cell.minute ?? ""), COLOR.ink3, true)] };
    }
    return figure(wrong > 0 ? `−${wrong}` : "", COLOR.ink2);
  }
  const attempts = cell.attempts ?? 0;
  if (!cell.solved && attempts === 0) return figure("");
  const earned = cell.points !== undefined ? String(cell.points) : cell.solved ? "+" : "";
  return { stack: [figure(earned, cell.solved ? COLOR.accent : COLOR.ink2), figure(String(attempts), COLOR.ink3, true)] };
}

/** A figure in the mono face; `under` is a second, tighter line below the first. */
type Figure = (value: string, color?: string, under?: boolean) => Content;

/**
 * Figures set in the mono face beside text in `steps.text`: the first line sits
 * on the text's baseline and takes the text's line height, so a row of figures
 * and names lines up; a line under it is set solid.
 */
function figures(steps: { text: Step; data: number }): Figure {
  const line = steps.text.size * steps.text.leading;
  const drop = ASCENDER[steps.text.face] * steps.text.size - ASCENDER.JetBrainsMono * steps.data;
  const natural = NATURAL.JetBrainsMono * steps.data;
  return (value, color = COLOR.ink, under = false) => ({
    text: value,
    font: "JetBrainsMono",
    fontSize: steps.data,
    color,
    ...(under ? { lineHeight: 1 } : { lineHeight: (line - drop) / natural, margin: [0, drop, 0, 0] }),
  });
}

/** The results table with the report's columns, its header repeated on every page, and the legend of a detailed one. */
function resultsTable(input: ResultsPdfInput): Content[] {
  const { report, texts, language } = input;
  const columns = columnsOf(input);
  const header = columns.map((column) => ({
    text: column.label.toLocaleUpperCase(language),
    ...textStyle(STEP.label, COLOR.ink3),
    alignment: column.align,
  }));
  const body = report.rows.map((row) => columns.map((column) => ({ ...column.cell(row), alignment: column.align })));
  const table: Content = {
    table: {
      headerRows: 1,
      ...(body.length > 0 ? { keepWithHeaderRows: 1 } : {}),
      dontBreakRows: true,
      widths: columns.map((column) => column.width),
      body: [header, ...body],
    },
    layout: {
      hLineWidth: (i: number) => (i === 0 ? 0 : PX),
      hLineColor: (i: number) => (i === 1 ? COLOR.line2 : COLOR.line),
      vLineWidth: () => 0,
      paddingLeft: (i: number) => (i === 0 ? 0 : SPACE.cellRight / 2),
      paddingRight: (i: number, node: { table: { widths: unknown[] } }) =>
        i === node.table.widths.length - 1 ? 0 : SPACE.cellRight / 2,
      paddingTop: () => SPACE.cellY,
      paddingBottom: () => SPACE.cellY,
    },
    margin: [0, SPACE.h3, 0, 0],
  };

  const legends: string[] = [];
  if (report.detail === "detailed") legends.push(report.contest.scoring === "icpc" ? texts.legendIcpc : texts.legendPoints);
  if (report.bonusCredit) legends.push(texts.legendBonus);
  return [table, ...legends.map((legend) => ({ text: legend, ...textStyle(STEP.small, COLOR.ink3), margin: [0, SPACE.item, 0, 0] }))];
}

function pageNumber(page: number, pages: number): Content {
  return { text: `${page} / ${pages}`, ...textStyle(STEP.small, COLOR.ink3), alignment: "center" };
}

/** What both PDFs share: the page, the running header with the contest's title, and the file's own description. */
function frame(input: ResultsPdfInput, title: string): Content {
  const { report, language, logo } = input;
  const measure = measureOf(report);
  const contest = contestTitle(report, language);
  return {
    info: { title: contest ? `${title} — ${contest}` : title, creator: "DB Contest", producer: "DB Contest" },
    language,
    pageSize: "A4",
    ...(report.detail === "detailed" ? { pageOrientation: "landscape" } : {}),
    pageMargins: [PAGE.side, PAGE.top, PAGE.side, PAGE.bottom],
    defaultStyle: { font: "Onest", fontSize: STEP.body.size, color: COLOR.ink },
    images: logo && logo !== "mark" ? { logo: logo.src } : {},
    header: () => runningHeader(logo, input.headerTitle ?? contest, measure),
  };
}

/** "Results": the title, the contest's dates, the bands that apply, and the table. */
export function resultsTableDocument(input: ResultsPdfInput): Content {
  const { report, texts, timeZone } = input;
  const generated = pdfMoment(report.generatedAt, timeZone);
  return {
    ...frame(input, texts.table),
    content: [
      ...titleBlock(texts.table, input),
      ...datesLine(input),
      ...(report.status === "preliminary" ? [band(fill(texts.preliminary, { time: generated }))] : []),
      ...(report.hiddenByFreeze ? [band(texts.hiddenByFreeze)] : []),
      ...resultsTable(input),
    ],
    footer: (page: number, pages: number) => ({ ...pageNumber(page, pages), margin: [PAGE.side, PAGE.footer, PAGE.side, 0] }),
  };
}

function rulesBlock({ report, texts, language }: ResultsPdfInput): Content {
  const { scoring, icpcPenaltyMin, prizePlaces } = report.contest;
  const lines = [texts.scoring[scoring]];
  if (scoring === "icpc") lines.push(plural(texts.icpcPenalty, icpcPenaltyMin, language));
  if (scoring === "winner") lines.push(texts.prizePlacesWinner);
  else if (prizePlaces.first + prizePlaces.second + prizePlaces.third === 0) lines.push(texts.noPrizePlaces);
  else lines.push(fill(texts.prizePlaces, prizePlaces));
  return { stack: headed(heading(texts.rules, STEP.h3, SPACE.h3), lines.map((line) => paragraph(line))) };
}

function winnersBlock({ report, texts }: ResultsPdfInput): Content {
  const groups = ([1, 2, 3] as const)
    .map((prize) => ({ prize, names: report.rows.filter((row) => row.prize === prize).map((row) => nameOf(row, texts)) }))
    .filter((group) => group.names.length > 0);
  const label = { 1: texts.prizes.first, 2: texts.prizes.second, 3: texts.prizes.third };
  return {
    stack: headed(
      heading(texts.winners, STEP.h3, SPACE.h3),
      groups.length === 0
        ? [paragraph(texts.noWinners, COLOR.ink2)]
        : groups.flatMap((group) =>
            headed(
              heading(label[group.prize], STEP.row, SPACE.block),
              group.names.map((name) => paragraph(name, COLOR.ink, 0)),
            ),
          ),
    ),
  };
}

/** How long a line to sign on is, and the room above it to sign in. */
const SIGNATURE = { width: 170, room: SPACE.h2 } as const;

/** How far below a body line's top a rule to write on sits: just under the text's baseline. */
const UNDER_BASELINE = ASCENDER[STEP.body.face] * STEP.body.size + SPACE.cellY;

function rule(width: number): Content {
  return { canvas: [{ type: "line", x1: 0, y1: 0, x2: width, y2: 0, lineWidth: SPACE.rule * 2, lineColor: COLOR.ink3 }] };
}

/** A typed name on the left and, on the right, a line to sign on with its caption under it. */
function signatureLine(name: string, texts: ResultsPdfTexts): Content {
  return {
    unbreakable: true,
    columns: [
      { text: name, ...textStyle(STEP.body), width: "*", margin: [0, SIGNATURE.room, 0, 0] },
      {
        width: SIGNATURE.width,
        stack: [
          { ...rule(SIGNATURE.width), margin: [0, SIGNATURE.room + UNDER_BASELINE, 0, 0] },
          { text: texts.signature, ...textStyle(STEP.small, COLOR.ink3), alignment: "center" },
        ],
      },
    ],
    columnGap: SPACE.block,
  };
}

function juryBlock({ jury, texts }: ResultsPdfInput): Content {
  const chair = jury?.chair.trim() ?? "";
  const members = (jury?.members ?? []).map((name) => name.trim()).filter(Boolean);
  return {
    stack: [
      ...headed(heading(texts.chair, STEP.row, SPACE.h3), [signatureLine(chair, texts)]),
      ...headed(
        heading(texts.members, STEP.row, SPACE.h3),
        (members.length > 0 ? members : [""]).map((name) => signatureLine(name, texts)),
      ),
      {
        columns: [
          { text: texts.date, ...textStyle(STEP.body), width: "auto" },
          { ...rule(SIGNATURE.width / 2), width: "auto", margin: [0, UNDER_BASELINE, 0, 0] },
        ],
        columnGap: SPACE.block,
        margin: [0, SPACE.h3, 0, 0],
      },
    ],
  };
}

/**
 * "Results protocol", a signed-off record: refused for a preliminary report,
 * which can still change. The header names the installation and counts the
 * participants; then the rules, the table, the prize winners by name, the
 * jury's lines to sign, and a footer carrying the generation time and the
 * report's fingerprint so two printouts can be compared.
 */
export function resultsProtocolDocument(input: ResultsPdfInput): Content {
  const { report, texts, timeZone, installationName } = input;
  if (report.status === "preliminary") throw new Error("a results protocol needs a final or archived report");
  const generated = pdfMoment(report.generatedAt, timeZone);
  const small = textStyle(STEP.small, COLOR.ink3);
  return {
    ...frame(input, texts.protocol),
    content: [
      {
        stack: [
          { text: installationName, ...textStyle(STEP.small, COLOR.ink2), margin: [0, 0, 0, SPACE.item] },
          ...titleBlock(texts.protocol, input),
          ...datesLine(input),
          paragraph(fill(texts.participants, { selected: report.rows.length, registered: report.registered }), COLOR.ink2),
          ...(report.status === "final" ? [band(fill(texts.notArchived, { time: generated }))] : []),
          ...(report.hiddenByFreeze ? [band(texts.hiddenByFreeze)] : []),
        ],
      },
      rulesBlock(input),
      { stack: resultsTable(input) },
      winnersBlock(input),
      juryBlock(input),
    ],
    footer: (page: number, pages: number) => ({
      columns: [
        { text: fill(texts.generatedAt, { time: generated }), ...small, width: "*" },
        { ...pageNumber(page, pages), width: "auto" },
        { text: fill(texts.fingerprint, { fingerprint: report.fingerprint }), ...small, alignment: "right", width: "*" },
      ],
      columnGap: SPACE.block,
      margin: [PAGE.side, PAGE.footer, PAGE.side, 0],
    }),
  };
}
