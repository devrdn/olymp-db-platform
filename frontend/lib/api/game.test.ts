import { describe, expect, test } from "vitest";

import { gameSchema, uploadLimitsSchema, uploadSchema, uploadWindowSchema } from "./game";

const limits = { enabled: true, chunk_bytes: 8 * 1024 * 1024, max_file_bytes: 4 * 1024 * 1024 * 1024 };

describe("uploadLimitsSchema", () => {
  test("reads the pair of ceilings a browser needs before slicing a file", () => {
    expect(uploadLimitsSchema.parse(limits)).toEqual({
      enabled: true,
      chunkBytes: 8 * 1024 * 1024,
      maxFileBytes: 4 * 1024 * 1024 * 1024,
    });
  });

  test("carries a genuine zero ceiling rather than mistaking it for disabled", () => {
    const parsed = uploadLimitsSchema.parse({ enabled: true, chunk_bytes: 0, max_file_bytes: 0 });

    expect(parsed).toEqual({ enabled: true, chunkBytes: 0, maxFileBytes: 0 });
  });
});

describe("gameSchema", () => {
  const game = {
    status: "ready",
    version: 2,
    database: "game_tpl_cabc",
    build_error: "",
    script_bytes: 128,
    building: false,
    updated_at: "2026-03-01T09:00:00Z",
    upload_limits: limits,
  };

  // Written out field for field as the API sends it (no `omitempty`): a
  // fixture that omits `source` hides that the API sends "".
  test("parses the reply a contest with no game actually sends", () => {
    const parsed = gameSchema.parse({
      status: "absent",
      version: 0,
      database: "",
      source: "",
      build_error: "",
      script_bytes: 0,
      building: false,
      updated_at: "0001-01-01T00:00:00Z",
      upload_limits: limits,
    });

    expect(parsed.status).toBe("absent");
    expect(parsed.source).toBe("editor");
  });

  // An API older than the source field.
  test("reads a reply with no source field as an editor-written game", () => {
    expect(gameSchema.parse(game).source).toBe("editor");
  });

  // A full reply for the table builder, every field the server sends.
  test("parses a real reply for a game built with the table builder", () => {
    const parsed = gameSchema.parse({
      status: "pending",
      version: 1,
      database: "game_tpl_cabc",
      source: "builder",
      build_error: "",
      script_bytes: 0,
      building: true,
      updated_at: "2026-03-01T09:00:00Z",
      upload_limits: limits,
      builder_limits: {
        enabled: true,
        chunk_bytes: 8 * 1024 * 1024,
        max_file_bytes: 4 * 1024 * 1024 * 1024,
        max_tables: 50,
        max_table_columns: 50,
        max_definition_bytes: 65536,
        max_field_bytes: 65536,
        max_line_bytes: 4 * 1024 * 1024,
        max_rows: 200_000,
        max_deleted_rows: 10_000,
        column_types: ["integer", "text", "date", "timestamp", "numeric", "boolean"],
      },
    });

    expect(parsed.status).toBe("pending");
    expect(parsed.source).toBe("builder");
    expect(parsed.upload).toBeUndefined();
  });

  test("carries the upload ceilings alongside the build's own status", () => {
    const parsed = gameSchema.parse(game);

    expect(parsed.uploadLimits).toEqual({ enabled: true, chunkBytes: 8388608, maxFileBytes: 4294967296 });
  });

  test("defaults to disabled when the field is missing", () => {
    const rest: Record<string, unknown> = { ...game };
    delete rest.upload_limits;
    const parsed = gameSchema.parse(rest);

    expect(parsed.uploadLimits).toEqual({ enabled: false, chunkBytes: 0, maxFileBytes: 0 });
  });
});

describe("uploadSchema", () => {
  const upload = {
    id: "11111111-1111-1111-1111-111111111111",
    filename: "contest.dump.sql",
    declared_bytes: 3_000_000_000,
    received_bytes: 1_500_000_000,
    sha256: "",
    lines: 0,
    status: "receiving",
    created_at: "2026-03-01T09:00:00Z",
    updated_at: "2026-03-01T09:05:00Z",
    upload_limits: limits,
  };

  test("reads an upload in progress, camelCased", () => {
    const parsed = uploadSchema.parse(upload);

    expect(parsed).toMatchObject({
      id: "11111111-1111-1111-1111-111111111111",
      filename: "contest.dump.sql",
      declaredBytes: 3_000_000_000,
      receivedBytes: 1_500_000_000,
      status: "receiving",
    });
  });

  test("reads the handler's own sentinel for no upload in progress", () => {
    const parsed = uploadSchema.parse({ ...upload, status: "absent", received_bytes: 0 });

    expect(parsed.status).toBe("absent");
  });

  test("refuses a status the server has no business sending", () => {
    expect(() => uploadSchema.parse({ ...upload, status: "sideways" })).toThrow();
  });
});

describe("uploadWindowSchema", () => {
  test("reads a slice of a completed upload's lines", () => {
    const parsed = uploadWindowSchema.parse({
      from_line: 41,
      lines: ["CREATE TABLE guests (", "  id uuid PRIMARY KEY"],
      total_lines: 12_000,
      truncated: false,
    });

    expect(parsed).toEqual({
      fromLine: 41,
      lines: ["CREATE TABLE guests (", "  id uuid PRIMARY KEY"],
      totalLines: 12_000,
      truncated: false,
    });
  });

  // The API never sends null for a list; accepting one would hide the day it did.
  test("refuses a null line list rather than rendering nothing", () => {
    expect(() =>
      uploadWindowSchema.parse({ from_line: 1, lines: null, total_lines: 0, truncated: false }),
    ).toThrow();
  });
});
