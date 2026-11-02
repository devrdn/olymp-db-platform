import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { ResultsReport, ResultsRow } from "@/lib/api/results";
import { formatTime } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import type { ResultsPdfTexts } from "./pdf/results-document";

const { fetchResultsAction, downloadResultsCsv, fetchResultsReport, makeResultsPdf, saveBlob, pdfModule } =
  vi.hoisted(() => ({
    fetchResultsAction: vi.fn(),
    downloadResultsCsv: vi.fn(),
    fetchResultsReport: vi.fn(),
    makeResultsPdf: vi.fn(),
    saveBlob: vi.fn(),
    pdfModule: { evaluated: false },
  }));
vi.mock("./actions", () => ({ fetchResultsAction }));
vi.mock("./requests", () => ({ downloadResultsCsv, fetchResultsReport }));
vi.mock("./pdf/make-results-pdf", () => {
  pdfModule.evaluated = true;
  return { makeResultsPdf };
});
vi.mock("@/lib/download", () => ({ saveBlob }));

import { resultsFileName } from "./request";
import { ExportForm } from "./export-form";

const ID = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

let dict: Dictionary;
let pdfTexts: Record<Locale, ResultsPdfTexts>;
beforeAll(async () => {
  dict = await getDictionary("en");
  const [ro, ru] = await Promise.all([getDictionary("ro"), getDictionary("ru")]);
  pdfTexts = { en: dict.leaderboard.export.pdf, ro: ro.leaderboard.export.pdf, ru: ru.leaderboard.export.pdf };
});

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
    penalty: 0,
    points: null,
    lastSolvedAt: null,
    bonusCredit: null,
    cells: [],
    ...over,
  };
}

function report(over: Partial<ResultsReport> = {}): ResultsReport {
  return {
    contest: {
      id: ID,
      titles: { ro: "Olimpiada", en: "Olympiad" },
      defaultLanguage: "ro",
      status: "running",
      scoring: "icpc",
      icpcPenaltyMin: 20,
      prizePlaces: { first: 1, second: 1, third: 0 },
      startsAt: "2026-11-02T08:00:00Z",
      endsAt: "2026-11-02T12:00:00Z",
    },
    status: "preliminary",
    hiddenByFreeze: false,
    generatedAt: "2026-11-02T10:15:00Z",
    registered: 4,
    detail: "summary",
    fields: ["full_name", "login"],
    bonusCredit: false,
    questions: [],
    rows: [
      row("r1", { place: 1, prize: 1, fullName: "Ana Pop", login: "apop", solved: 4, penalty: 120 }),
      row("r2", { place: 2, prize: 2, fullName: "Ion Rus", login: "irus", solved: 3, penalty: 95 }),
      row("r3", { place: 3, fullName: "Maria Ceban", login: "mceban", solved: 2, penalty: 60 }),
      row("r4", { disqualified: true, fullName: "Dan Popa", login: "dpopa", solved: 5, penalty: 10 }),
    ],
    fingerprint: "a1b2c3d4",
    mayExportEmail: false,
    ...over,
  };
}

const finished = (over: Partial<ResultsReport> = {}) =>
  report({ status: "final", contest: { ...report().contest, status: "finished" }, ...over });

function show(value: ResultsReport = report()) {
  return render(
    <ExportForm
      contestId={ID}
      report={value}
      dict={dict}
      locale="en"
      pdfTexts={pdfTexts}
      installationName="Universitatea de Stat din Moldova"
      logoHref="/api/v1/branding/logo?v=1"
    />,
  );
}

const t = () => dict.leaderboard.export;
const download = (kind: "csv" | "table" | "protocol", n: number) =>
  screen.getByRole("button", { name: t().download[kind].replace("{n}", String(n)) });

beforeEach(() => {
  for (const mock of [fetchResultsAction, downloadResultsCsv, fetchResultsReport, makeResultsPdf, saveBlob]) {
    mock.mockReset();
  }
});

describe("the export form", () => {
  test("loads no PDF module until a PDF is asked for", async () => {
    // A fresh module registry, so an earlier test's import cannot have evaluated it already.
    vi.resetModules();
    pdfModule.evaluated = false;
    const json = finished();
    fetchResultsReport.mockResolvedValue({ kind: "ok", report: json });
    makeResultsPdf.mockResolvedValue(new Blob(["%PDF"]));
    show();

    expect(pdfModule.evaluated).toBe(false);

    await userEvent.click(screen.getByRole("radio", { name: t().formats.table }));
    expect(pdfModule.evaluated).toBe(false);

    await userEvent.click(download("table", 3));
    await vi.waitFor(() => expect(saveBlob).toHaveBeenCalled());
    expect(pdfModule.evaluated).toBe(true);
  });

  test("the download button names the format and counts the ticked rows", async () => {
    show();

    expect(download("csv", 3)).toBeEnabled();

    await userEvent.click(screen.getByRole("checkbox", { name: "Ion Rus" }));
    expect(download("csv", 2)).toBeEnabled();

    await userEvent.click(screen.getByRole("radio", { name: t().formats.table }));
    expect(download("table", 2)).toBeEnabled();

    await userEvent.click(screen.getByRole("button", { name: t().none }));
    expect(download("table", 0)).toBeDisabled();
    expect(screen.getByText(t().tickOne)).toBeInTheDocument();
  });

  test("the protocol is disabled with its reason while the contest runs", () => {
    const { unmount } = show();

    expect(screen.getByRole("radio", { name: t().formats.protocol })).toBeDisabled();
    expect(screen.getByText(t().protocolUnavailable)).toBeInTheDocument();
    unmount();

    show(finished());
    expect(screen.getByRole("radio", { name: t().formats.protocol })).toBeEnabled();
    expect(screen.queryByText(t().protocolUnavailable)).not.toBeInTheDocument();
  });

  test("email is offered only when the server allows it", () => {
    const { unmount } = show();
    expect(screen.queryByRole("checkbox", { name: t().fields.email })).not.toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: t().fields.full_name })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: t().fields.login })).toBeChecked();
    unmount();

    show(report({ mayExportEmail: true }));
    expect(screen.getByRole("checkbox", { name: t().fields.email })).not.toBeChecked();
  });

  test("keeps at least one identity column, since the export refuses none", async () => {
    show();

    await userEvent.click(screen.getByRole("checkbox", { name: t().fields.login }));

    expect(screen.getByRole("checkbox", { name: t().fields.full_name })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: t().fields.full_name })).toHaveAttribute("aria-disabled", "true");
  });

  test("Excel is offered only for CSV, the language only for PDFs, jury names only for the protocol", async () => {
    show(finished());

    expect(screen.getByRole("checkbox", { name: t().excel })).not.toBeChecked();
    expect(screen.queryByRole("combobox", { name: t().language })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: t().chair })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: t().formats.table }));
    expect(screen.queryByRole("checkbox", { name: t().excel })).not.toBeInTheDocument();
    // The contest's own default language, which is one the PDF speaks.
    expect(screen.getByRole("combobox", { name: t().language })).toHaveValue("ro");
    expect(screen.queryByRole("textbox", { name: t().chair })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: t().formats.protocol }));
    expect(screen.getByRole("combobox", { name: t().language })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: t().chair })).toHaveValue("");
    expect(screen.getByRole("textbox", { name: t().members })).toHaveValue("");
  });

  test("prints a PDF in the interface language when the contest's own is not one the PDF speaks", async () => {
    show(report({ contest: { ...report().contest, defaultLanguage: "de" } }));

    await userEvent.click(screen.getByRole("radio", { name: t().formats.table }));

    expect(screen.getByRole("combobox", { name: t().language })).toHaveValue("en");
  });

  test("a CSV download sends the chosen rows and options and saves the file under the server's name", async () => {
    const blob = new Blob(["place\r\n"]);
    downloadResultsCsv.mockResolvedValue({ kind: "ok", blob, fileName: "results-server.csv" });
    show();

    await userEvent.click(screen.getByRole("checkbox", { name: "Ion Rus" }));
    await userEvent.click(screen.getByRole("radio", { name: t().details.detailed }));
    await userEvent.click(screen.getByRole("checkbox", { name: t().excel }));
    await userEvent.click(download("csv", 2));

    expect(downloadResultsCsv).toHaveBeenCalledWith(ID, {
      format: "csv",
      registrations: ["r1", "r3"],
      detail: "detailed",
      fields: ["full_name", "login"],
      csv: { excel: true },
    });
    await vi.waitFor(() => expect(saveBlob).toHaveBeenCalledWith(blob, "results-server.csv"));
  });

  test("a PDF download asks for JSON, makes the PDF in the chosen language and saves it", async () => {
    const json = finished({ generatedAt: "2026-11-02T12:30:00Z" });
    const pdf = new Blob(["%PDF"]);
    fetchResultsReport.mockResolvedValue({ kind: "ok", report: json });
    makeResultsPdf.mockResolvedValue(pdf);
    show(finished());

    await userEvent.click(screen.getByRole("radio", { name: t().formats.protocol }));
    await userEvent.selectOptions(screen.getByRole("combobox", { name: t().language }), "ru");
    await userEvent.type(screen.getByRole("textbox", { name: t().chair }), "Elena Rusu");
    await userEvent.type(screen.getByRole("textbox", { name: t().members }), "Ion Ceban{enter}{enter} Ana Lupu ");
    await userEvent.click(download("protocol", 3));

    expect(fetchResultsReport).toHaveBeenCalledWith(ID, {
      format: "json",
      registrations: ["r1", "r2", "r3"],
      detail: "summary",
      fields: ["full_name", "login"],
    });
    await vi.waitFor(() => expect(saveBlob).toHaveBeenCalledWith(pdf, resultsFileName("protocol", ID, json.generatedAt)));
    expect(makeResultsPdf).toHaveBeenCalledWith("protocol", {
      report: json,
      language: "ru",
      texts: pdfTexts.ru,
      installationName: "Universitatea de Stat din Moldova",
      timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      jury: { chair: "Elena Rusu", members: ["Ion Ceban", "Ana Lupu"] },
      logoHref: "/api/v1/branding/logo?v=1",
    });
  });

  test("a refused export shows the dictionary's sentence for its code", async () => {
    downloadResultsCsv.mockResolvedValue({ kind: "refused", code: "results_export_too_often" });
    show();

    await userEvent.click(download("csv", 3));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.results_export_too_often);
    expect(saveBlob).not.toHaveBeenCalled();
  });

  test("a PDF that cannot be made says so", async () => {
    fetchResultsReport.mockResolvedValue({ kind: "ok", report: report() });
    makeResultsPdf.mockRejectedValue(new Error("font"));
    show();

    await userEvent.click(screen.getByRole("radio", { name: t().formats.table }));
    await userEvent.click(download("table", 3));

    expect(await screen.findByRole("alert")).toHaveTextContent(t().failed);
  });

  test("a CSV that cannot be saved says the file failed, not the PDF", async () => {
    downloadResultsCsv.mockResolvedValue({ kind: "ok", blob: new Blob(["x"]), fileName: "results.csv" });
    saveBlob.mockImplementation(() => {
      throw new Error("blocked");
    });
    show();

    await userEvent.click(download("csv", 3));

    expect(await screen.findByRole("alert")).toHaveTextContent(t().failed);
    expect(t().failed).not.toMatch(/PDF/);
  });

  test("pins the download bar to the viewport whenever the form is one column, not only on a phone", () => {
    show();

    // The same container condition that puts the form in one column.
    const bar = download("csv", 3).parentElement as HTMLElement;
    expect(bar.className).toContain("@max-3xl/export:fixed");
    expect(bar.className).not.toContain("max-narrow:fixed");
    expect(bar.closest(".\\@container\\/export")).not.toBeNull();
  });

  test("refresh keeps the ticks of rows still listed", async () => {
    const later = report({
      generatedAt: "2026-11-02T10:40:00Z",
      rows: [
        row("r1", { place: 1, prize: 1, fullName: "Ana Pop", login: "apop" }),
        row("r2", { place: 2, prize: 2, fullName: "Ion Rus", login: "irus" }),
        row("r5", { place: 3, fullName: "Vlad Lungu", login: "vlungu" }),
      ],
    });
    fetchResultsAction.mockResolvedValue({ kind: "ok", report: later });
    show();

    await userEvent.click(screen.getByRole("checkbox", { name: "Ana Pop" }));
    await userEvent.click(screen.getByRole("button", { name: t().refresh }));

    expect(fetchResultsAction).toHaveBeenCalledWith(ID);
    expect(await screen.findByText("Vlad Lungu")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Ana Pop" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Ion Rus" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Vlad Lungu" })).not.toBeChecked();
    expect(download("csv", 1)).toBeEnabled();
    expect(
      screen.getByText(t().asOf.replace("{time}", formatTime("2026-11-02T10:40:00Z", { locale: "en" }))),
    ).toBeInTheDocument();
  });

  test("a refused refresh keeps the list and says why", async () => {
    fetchResultsAction.mockResolvedValue({ kind: "refused", code: "results_too_many_rows" });
    show();

    await userEvent.click(screen.getByRole("button", { name: t().refresh }));

    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.results_too_many_rows);
    expect(screen.getByText("Ana Pop")).toBeInTheDocument();
  });

  test("shows the running, archive and freeze warnings as they apply", () => {
    const running = show();
    expect(screen.getByText(t().preliminary)).toBeInTheDocument();
    expect(screen.queryByText(t().notArchived)).not.toBeInTheDocument();
    expect(screen.queryByText(t().hiddenByFreeze)).not.toBeInTheDocument();
    running.unmount();

    const ended = show(finished({ hiddenByFreeze: true }));
    expect(screen.queryByText(t().preliminary)).not.toBeInTheDocument();
    expect(screen.getByText(t().notArchived)).toBeInTheDocument();
    expect(screen.getByText(t().hiddenByFreeze)).toBeInTheDocument();
    ended.unmount();

    show(report({ status: "archived", contest: { ...report().contest, status: "archived" } }));
    const warnings = [t().preliminary, t().notArchived, t().hiddenByFreeze];
    for (const warning of warnings) expect(screen.queryByText(warning)).not.toBeInTheDocument();
  });

  test("states the moment the list was read", () => {
    show();

    const stamp = screen.getByText(t().asOf.replace("{time}", formatTime("2026-11-02T10:15:00Z", { locale: "en" })));
    expect(within(stamp.parentElement as HTMLElement).getByRole("button", { name: t().refresh })).toBeInTheDocument();
  });
});
