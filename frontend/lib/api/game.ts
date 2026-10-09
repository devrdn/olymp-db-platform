import { z } from "zod";

/**
 * A contest's game database, as staff manage it. Status and script are
 * separate reads: the status is polled during a build, and the script can be
 * half a mebibyte.
 */

/** The states of a contest's game database. */
export const GAME_STATUSES = [
  /** No script has ever been written. Every contest starts here. */
  "absent",
  /** A script is stored and the build has not run yet. */
  "pending",
  "building",
  /** The template exists and participants' copies can be made from it. */
  "ready",
  /** The build did not finish; `buildError` says why. */
  "failed",
  /** The reclaim sweep removed the template after the contest ended. */
  "dropped",
] as const;

export type GameStatus = (typeof GAME_STATUSES)[number];

/**
 * The ceilings a chunked dump upload must respect. Check `enabled` first: with
 * `GAME_UPLOAD_DIR` unset both numbers are zero, which is otherwise a valid
 * ceiling.
 */
export const uploadLimitsSchema = z
  .object({
    enabled: z.boolean(),
    chunk_bytes: z.number(),
    max_file_bytes: z.number(),
  })
  .transform((raw) => ({
    enabled: raw.enabled,
    chunkBytes: raw.chunk_bytes,
    maxFileBytes: raw.max_file_bytes,
  }));

export type UploadLimits = z.infer<typeof uploadLimitsSchema>;

/** CamelCased because zod applies a `.default()` value without running `.transform()`. */
const DISABLED_UPLOAD_LIMITS = { enabled: false, chunkBytes: 0, maxFileBytes: 0 };

/**
 * How the game was built: SQL typed in the editor, an uploaded dump, or the
 * table builder's structural description (`definitionSchema`).
 */
export const GAME_SOURCES = ["editor", "file", "builder"] as const;

export type GameSource = (typeof GAME_SOURCES)[number];

/**
 * The file a file-sourced game was built from, so the viewer can reopen it
 * after a reload: `GET .../uploads/current` names only an upload still
 * receiving, never a completed one.
 */
export const gameUploadSourceSchema = z
  .object({
    id: z.string(),
    filename: z.string(),
    bytes: z.number(),
    lines: z.number(),
  })
  .transform((raw) => ({ id: raw.id, filename: raw.filename, bytes: raw.bytes, lines: raw.lines }));

export type GameUploadSource = z.infer<typeof gameUploadSourceSchema>;

export const gameSchema = z
  .object({
    status: z.enum(GAME_STATUSES),
    version: z.number().default(0),
    database: z.string().default(""),
    // The API sends "" for a contest with no game yet (no `omitempty`), and a
    // bare `.default()` fires only on `undefined`, so both are coerced.
    source: z.preprocess(
      (value) => (value === "" || value === undefined ? "editor" : value),
      z.enum(GAME_SOURCES),
    ),
    // Absent for an editor-sourced game, so "no file" stays distinct.
    upload: gameUploadSourceSchema.optional(),
    build_error: z.string().default(""),
    script_bytes: z.number().default(0),
    // Read from the server, not kept as a constant here (CLAUDE.md rule 11).
    // Zero means "the server did not say", not a ceiling of nothing.
    max_script_bytes: z.number().default(0),
    building: z.boolean().default(false),
    updated_at: z.string().optional(),
    // Defaults here keep an older API from failing the whole render.
    needs_build: z.boolean().default(false),
    upload_limits: uploadLimitsSchema.default(DISABLED_UPLOAD_LIMITS),
  })
  .transform((raw) => ({
    status: raw.status,
    version: raw.version,
    database: raw.database,
    source: raw.source,
    upload: raw.upload,
    buildError: raw.build_error,
    scriptBytes: raw.script_bytes,
    maxScriptBytes: raw.max_script_bytes,
    building: raw.building,
    updatedAt: raw.updated_at ?? "",
    needsBuild: raw.needs_build,
    uploadLimits: raw.upload_limits,
  }));

export type Game = z.infer<typeof gameSchema>;

export const gameScriptSchema = z.object({ script: z.string() });

/**
 * States of a chunked dump upload, sent in pieces so a multi-gigabyte file
 * never has to fit in memory. `"absent"` is sent only by
 * `GET .../uploads/current` when nothing is in progress; no real upload
 * passes through it.
 */
export const UPLOAD_STATUSES = ["absent", "receiving", "complete", "aborted"] as const;

export type UploadStatus = (typeof UPLOAD_STATUSES)[number];

export const uploadSchema = z
  .object({
    id: z.string(),
    filename: z.string(),
    declared_bytes: z.number(),
    received_bytes: z.number(),
    // Empty until the upload is complete.
    sha256: z.string(),
    lines: z.number(),
    status: z.enum(UPLOAD_STATUSES),
    created_at: z.string(),
    updated_at: z.string(),
    upload_limits: uploadLimitsSchema,
  })
  .transform((raw) => ({
    id: raw.id,
    filename: raw.filename,
    declaredBytes: raw.declared_bytes,
    receivedBytes: raw.received_bytes,
    sha256: raw.sha256,
    lines: raw.lines,
    status: raw.status,
    createdAt: raw.created_at,
    updatedAt: raw.updated_at,
    uploadLimits: raw.upload_limits,
  }));

export type Upload = z.infer<typeof uploadSchema>;

/**
 * A slice of a completed upload's lines, for previewing a dump. Never the
 * whole file: one line can run to megabytes.
 */
export const uploadWindowSchema = z
  .object({
    from_line: z.number(),
    lines: z.array(z.string()),
    total_lines: z.number(),
    // The byte budget ran out, possibly mid-line, so the last string may be
    // partial. Reaching the end of the file is not truncation.
    truncated: z.boolean(),
  })
  .transform((raw) => ({
    fromLine: raw.from_line,
    lines: raw.lines,
    totalLines: raw.total_lines,
    truncated: raw.truncated,
  }));

export type UploadWindow = z.infer<typeof uploadWindowSchema>;

/**
 * The databases a contest owns: its spare pool and participants' copies. A
 * separate read from the status, which is polled every two seconds during a
 * build; this is a row per participant.
 */
export const GAME_INSTANCE_STATUSES = [
  "provisioning",
  "ready",
  "failed",
  /** The database is gone from the cluster; the row survives as history. */
  "dropped",
] as const;

export type GameInstanceStatus = (typeof GAME_INSTANCE_STATUSES)[number];

const gameInstanceSchema = z
  .object({
    database: z.string(),
    spare: z.boolean().default(false),
    registration_id: z.string().default(""),
    participant: z.string().default(""),
    participant_name: z.string().default(""),
    template_version: z.number().default(0),
    status: z.enum(GAME_INSTANCE_STATUSES),
    size_bytes: z.number().default(0),
    // A size the game cluster could not report must not show as zero bytes.
    size_known: z.boolean().default(false),
    created_at: z.string().default(""),
    updated_at: z.string().default(""),
  })
  .transform((raw) => ({
    database: raw.database,
    spare: raw.spare,
    registrationId: raw.registration_id,
    participant: raw.participant,
    participantName: raw.participant_name,
    templateVersion: raw.template_version,
    status: raw.status,
    sizeBytes: raw.size_bytes,
    sizeKnown: raw.size_known,
    createdAt: raw.created_at,
    updatedAt: raw.updated_at,
  }));

export type GameInstance = z.infer<typeof gameInstanceSchema>;

export const gameInstancesSchema = z
  .object({
    instances: z.array(gameInstanceSchema).default([]),
    truncated: z.boolean().default(false),
  })
  .transform((raw) => ({ instances: raw.instances, truncated: raw.truncated }));

export type GameInstances = z.infer<typeof gameInstancesSchema>;

// --- The table builder: tables and columns described directly, with data
// loaded as CSV or typed a row at a time, instead of SQL.

/**
 * The table builder's ceilings. Every limit and the column-type list come
 * from the server, never a client-side copy (CLAUDE.md rule 11). As with
 * `uploadLimitsSchema`, check `enabled` before the byte limits mean anything.
 */
export const builderLimitsSchema = z
  .object({
    enabled: z.boolean(),
    chunk_bytes: z.number(),
    max_file_bytes: z.number(),
    max_tables: z.number(),
    max_table_columns: z.number(),
    max_definition_bytes: z.number(),
    max_field_bytes: z.number(),
    max_line_bytes: z.number(),
    max_rows: z.number(),
    max_deleted_rows: z.number(),
    // `provisioning.ColumnTypes`, in declaration order.
    column_types: z.array(z.string()).default([]),
  })
  .transform((raw) => ({
    enabled: raw.enabled,
    chunkBytes: raw.chunk_bytes,
    maxFileBytes: raw.max_file_bytes,
    maxTables: raw.max_tables,
    maxTableColumns: raw.max_table_columns,
    maxDefinitionBytes: raw.max_definition_bytes,
    maxFieldBytes: raw.max_field_bytes,
    maxLineBytes: raw.max_line_bytes,
    maxRows: raw.max_rows,
    maxDeletedRows: raw.max_deleted_rows,
    columnTypes: raw.column_types,
  }));

export type BuilderLimits = z.infer<typeof builderLimitsSchema>;

/**
 * One builder column, the same shape for `GET` and `PUT`. `type` is a plain
 * string: the valid set is `builderLimits.columnTypes`, read from the server,
 * which also validates it.
 */
export const columnDefinitionSchema = z
  .object({
    name: z.string(),
    type: z.string(),
    // Omitted on the wire for a NOT NULL column.
    nullable: z.boolean().default(false),
  })
  .transform((raw) => ({ name: raw.name, type: raw.type, nullable: raw.nullable }));

export type ColumnDefinition = z.infer<typeof columnDefinitionSchema>;

export const tableDefinitionSchema = z
  .object({
    name: z.string(),
    columns: z.array(columnDefinitionSchema).default([]),
    primary_key: z.array(z.string()).default([]),
  })
  .transform((raw) => ({ name: raw.name, columns: raw.columns, primaryKey: raw.primary_key }));

export type TableDefinition = z.infer<typeof tableDefinitionSchema>;

/**
 * The builder's table definitions, read and written whole: the document is
 * bounded at `builderLimits.maxDefinitionBytes`, so it needs no chunking.
 */
export const definitionSchema = z
  .object({
    tables: z.array(tableDefinitionSchema).default([]),
    builder_limits: builderLimitsSchema,
  })
  .transform((raw) => ({ tables: raw.tables, builderLimits: raw.builder_limits }));

export type GameDefinition = z.infer<typeof definitionSchema>;

/** As with `UPLOAD_STATUSES`, `"absent"` means no upload in progress. */
export const TABLE_DATA_STATUSES = ["absent", "receiving", "complete", "aborted"] as const;

export type TableDataStatus = (typeof TABLE_DATA_STATUSES)[number];

/**
 * One table's chunked CSV upload, or its data once complete. `deletedRows`
 * holds the tombstoned row numbers, not a count; `activeRows` is `lines`
 * minus those, computed by the server.
 */
export const tableDataSchema = z
  .object({
    id: z.string(),
    table: z.string(),
    declared_bytes: z.number(),
    received_bytes: z.number(),
    lines: z.number(),
    active_rows: z.number(),
    deleted_rows: z.array(z.number()).default([]),
    status: z.enum(TABLE_DATA_STATUSES),
    created_at: z.string(),
    updated_at: z.string(),
    builder_limits: builderLimitsSchema,
  })
  .transform((raw) => ({
    id: raw.id,
    table: raw.table,
    declaredBytes: raw.declared_bytes,
    receivedBytes: raw.received_bytes,
    lines: raw.lines,
    activeRows: raw.active_rows,
    deletedRows: raw.deleted_rows,
    status: raw.status,
    createdAt: raw.created_at,
    updatedAt: raw.updated_at,
    builderLimits: raw.builder_limits,
  }));

export type TableData = z.infer<typeof tableDataSchema>;

export const tableRowSchema = z
  .object({ row: z.number(), fields: z.array(z.string()).default([]) })
  .transform((raw) => ({ row: raw.row, fields: raw.fields }));

export type TableRow = z.infer<typeof tableRowSchema>;

/** A server-bounded page of a table's rows, for previewing it before a build. */
export const tableRowWindowSchema = z
  .object({
    from_row: z.number(),
    rows: z.array(tableRowSchema).default([]),
    total_rows: z.number(),
    truncated: z.boolean(),
  })
  .transform((raw) => ({
    fromRow: raw.from_row,
    rows: raw.rows,
    totalRows: raw.total_rows,
    truncated: raw.truncated,
  }));

export type TableRowWindow = z.infer<typeof tableRowWindowSchema>;
