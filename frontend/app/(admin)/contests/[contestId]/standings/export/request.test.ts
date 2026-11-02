import { describe, expect, test } from "vitest";

import type { ResultsField } from "@/lib/api/results";

import { exportBody, protocolAvailable, resultsFileName, type ExportOptions } from "./request";

const options = (over: Partial<ExportOptions> = {}): ExportOptions => ({
  kind: "csv",
  detail: "summary",
  fields: new Set<ResultsField>(["full_name"]),
  excel: false,
  ...over,
});

describe("exportBody", () => {
  test("a CSV request carries the Excel choice and the fields in their fixed order", () => {
    const body = exportBody(
      options({ detail: "detailed", excel: true, fields: new Set<ResultsField>(["email", "login", "full_name"]) }),
      ["r1", "r2"],
    );
    expect(body).toEqual({
      format: "csv",
      registrations: ["r1", "r2"],
      detail: "detailed",
      fields: ["full_name", "login", "email"],
      csv: { excel: true },
    });
  });

  test("a CSV request without the Excel choice says so", () => {
    expect(exportBody(options(), ["r1"]).csv).toEqual({ excel: false });
  });

  test("a PDF request asks for JSON and carries no CSV options", () => {
    for (const kind of ["table", "protocol"] as const) {
      const body = exportBody(options({ kind, excel: true }), ["r1"]);
      expect(body.format).toBe("json");
      expect("csv" in body).toBe(false);
    }
  });

  test("the request carries the ids as given", () => {
    const ids = ["r3", "r1"];
    expect(exportBody(options(), ids).registrations).toEqual(["r3", "r1"]);
  });
});

describe("protocolAvailable", () => {
  test("the protocol is available only for a final or archived report", () => {
    expect(protocolAvailable("preliminary")).toBe(false);
    expect(protocolAvailable("final")).toBe(true);
    expect(protocolAvailable("archived")).toBe(true);
  });
});

describe("resultsFileName", () => {
  test("file names carry the contest id and the UTC stamp of the report", () => {
    const at = "2026-11-02T12:10:00Z";
    expect(resultsFileName("csv", "c-1", at)).toBe("results-c-1-20261102T121000Z.csv");
    expect(resultsFileName("table", "c-1", at)).toBe("results-c-1-20261102T121000Z.pdf");
    expect(resultsFileName("protocol", "c-1", at)).toBe("protocol-c-1-20261102T121000Z.pdf");
  });

  test("a stamp with an offset is written in UTC", () => {
    expect(resultsFileName("csv", "c", "2026-11-02T14:10:00+02:00")).toBe("results-c-20261102T121000Z.csv");
  });
});
