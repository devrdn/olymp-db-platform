import { z } from "zod";

/**
 * A contest's game database, as staff manage it.
 *
 * Two reads, not one, and deliberately so: the status is polled while a build
 * runs, and the script is up to half a mebibyte. Carrying the script inside
 * the status would make every poll pay for it.
 */
export const GAME_STATUSES = [
  /** No script has ever been written. Every contest starts here. */
  "absent",
  /** A script is stored and the build has not run yet. */
  "pending",
  /** A build is under way. */
  "building",
  /** The template exists and participants' copies can be made from it. */
  "ready",
  /** The build ran and did not finish. `buildError` says why. */
  "failed",
  /** The reclaim sweep removed the template after the contest ended. */
  "dropped",
] as const;

export type GameStatus = (typeof GAME_STATUSES)[number];

/**
 * The ceilings a chunked upload must respect on this installation —
 * `game_handler.go`'s `uploadLimitsResponse`.
 *
 * `enabled` travels apart from the two numbers on purpose, and is checked
 * first: an installation with no upload volume configured
 * (`GAME_UPLOAD_DIR` unset) reports both as zero, and zero is also a ceiling
 * an operator could genuinely set. Without this field the two would be
 * indistinguishable — a client would have no way to tell "uploads are off,
 * do not offer the button" from "the operator set a ceiling of zero, which
 * refuses everything anyway".
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

/**
 * A ceiling of "uploads are off" — the shape `enabled: false` always takes.
 *
 * Already camelCased, not the wire shape: zod applies a `.default()` value
 * as-is, without re-running the schema's own `.transform()` over it, so a
 * default has to be written in whatever shape the field is read in.
 */
const DISABLED_UPLOAD_LIMITS = { enabled: false, chunkBytes: 0, maxFileBytes: 0 };

/**
 * Which of the three ways this game was built — `game_handler.go`'s
 * `gameResponse.Source`, itself `provisioning.Template.Source`. `"builder"`
 * is the table builder's own way in (`provisioning.SourceBuilder`), a
 * structural description rather than SQL typed or uploaded — its own
 * `definitionSchema`, below, is that description's wire shape.
 *
 * A game with no source at all reads as `"editor"`: an editor-sourced game
 * with no script is exactly what a contest with no game yet looks like on
 * this screen. That has to cover two different absences, which is why the
 * field is coerced rather than merely defaulted. `undefined` is a response
 * from an API older than this field. The empty string is what this API sends
 * *today* for the synthetic `absent` answer — `gameResponse.Source` is a
 * plain string with no `omitempty`, so a contest with no game carries
 * `"source": ""` — and a bare `.default()` fires only on `undefined`, so it
 * let that through to the enum and rejected the server's own reply. Every
 * newly created contest's game screen was an error boundary.
 */
export const GAME_SOURCES = ["editor", "file", "builder"] as const;

export type GameSource = (typeof GAME_SOURCES)[number];

/**
 * The file a file-sourced game (`source === "file"`) was built from — enough
 * to reopen the console's own viewer on it after a reload, which is the
 * whole reason this travels here rather than only inside `uploadResponse`
 * while the upload was still in progress: the page that ran the upload is
 * gone by the time somebody reloads, and `GET .../uploads/current` no longer
 * names a *completed* upload (`provisioning.Games.CurrentUpload` answers only
 * for one still `'receiving'`) — this is the one place left that still can.
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
    source: z.preprocess(
      (value) => (value === "" || value === undefined ? "editor" : value),
      z.enum(GAME_SOURCES),
    ),
    // Absent (not merely empty) for an editor-sourced game — `.optional()`
    // rather than a default so a client can tell "no file" from "a file
    // whose fields happen to be empty", the same distinction `upload_limits`
    // itself draws with its own `enabled` flag.
    upload: gameUploadSourceSchema.optional(),
    build_error: z.string().default(""),
    script_bytes: z.number().default(0),
    // The ceiling the API refuses a script past (provisioning.MaxScriptBytes,
    // published by game_handler.go on both the ordinary and the "absent"
    // answer). Read rather than kept as a constant here: a copy in this
    // bundle would go on refusing by the old number the day the server
    // raises it, with nothing on either side to notice (CLAUDE.md rule 11).
    //
    // Defaulted to zero for the same reason `upload_limits` is defaulted —
    // an older API that does not send it must not fail the whole render —
    // and zero is read as "the server did not say" by whoever uses it, not
    // as a ceiling of nothing.
    max_script_bytes: z.number().default(0),
    building: z.boolean().default(false),
    updated_at: z.string().optional(),
    // Defaulted rather than required: every current build of the API sends
    // it (game_handler.go's uploadLimitsResponse), but a page that has no
    // use for the file-upload half of this screen must not fail to render
    // over a field it does not read.
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
    uploadLimits: raw.upload_limits,
  }));

export type Game = z.infer<typeof gameSchema>;

export const gameScriptSchema = z.object({ script: z.string() });

/**
 * A game database built from a finished dump rather than a script typed in
 * the editor — the second way `POST .../game/uploads` through
 * `.../complete` lets an organiser produce the same thing `SetScript` does.
 *
 * A chunked upload, not a single request: the file is sent in pieces
 * (`PUT .../uploads/{id}/chunk?offset=N`) so a multi-gigabyte dump never has
 * to fit in the browser's memory, or this server's, all at once. `status`
 * carries a fourth value beyond `provisioning.UploadStatus`'s own three —
 * `"absent"` — that only `GET .../uploads/current` ever sends, for a contest
 * with no upload in progress; it is a handler-only sentinel, not a state a
 * real upload passes through.
 */
export const UPLOAD_STATUSES = ["absent", "receiving", "complete", "aborted"] as const;

export type UploadStatus = (typeof UPLOAD_STATUSES)[number];

export const uploadSchema = z
  .object({
    id: z.string(),
    filename: z.string(),
    declared_bytes: z.number(),
    received_bytes: z.number(),
    // Empty until the upload is complete — the checksum is computed in
    // Store.Complete's own one sequential pass over the finished file.
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
 * One slice of a completed upload's lines — the console's own preview of a
 * dump it has not run yet, the same role the script editor's own textarea
 * plays for one typed in directly. Never the whole file: a line of a real
 * dump can run to megabytes, and the file itself to gigabytes.
 */
export const uploadWindowSchema = z
  .object({
    from_line: z.number(),
    lines: z.array(z.string()),
    total_lines: z.number(),
    // The byte budget ran out before max_lines lines were collected, which
    // can happen mid-line — the last string in `lines` may not be a whole
    // one. Never means the window ran past the end of the file; that is an
    // ordinary short window, not a truncated one.
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
 * The databases a contest already owns: its spare pool and the participants'
 * own copies.
 *
 * A separate read from the game's status, and deliberately not folded into
 * it: the status is polled every two seconds while a build runs, and this is
 * a row per participant that nobody needs at that rate.
 */
export const GAME_INSTANCE_STATUSES = [
  /** The copy is being made. */
  "provisioning",
  /** The copy exists and can be worked in. */
  "ready",
  /** Making the copy did not finish. */
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
    // Apart from the number on purpose: the sizes come from the game cluster,
    // and one that could not be read must not reach the screen as a database
    // of zero bytes.
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

// --- The table builder: a structural description instead of SQL -----------
//
// The third way to build a contest's game (`GAME_SOURCES` above): tables and
// columns described directly, with data loaded as CSV or typed in a row at a
// time, rather than written as SQL. `game_handler.go`'s own comment names it
// the same way: "a structural description of its tables and columns instead
// of SQL, with data loaded as CSV".

/**
 * The ceilings the table builder's own third way must respect —
 * `game_handler.go`'s `builderLimitsResponse`. Every number a client uses to
 * slice a CSV chunk, cap a table or column count, or offer a type on a
 * column's picker comes from here — never a second copy kept on this side.
 * This is the field CLAUDE.md rule 11 names directly: "the chunk limit never
 * reached the browser, two independent ceilings stood on the same size, the
 * game's source never reached the status" is the exact history of defects a
 * client-side constant standing in for any one of these numbers would repeat.
 *
 * `enabled` is checked before `chunkBytes` or `maxFileBytes` mean anything,
 * the same convention `uploadLimitsSchema` draws for the dump's own pair: an
 * installation with no table-data volume configured reports both as zero,
 * which is also a ceiling an operator could genuinely set.
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
    // The closed set of column types this platform offers —
    // `provisioning.ColumnTypes`, as strings, in the order the domain
    // declares them. Read as data rather than kept as a second, hand-typed
    // list here: the whole reason `game_handler.go` walks that slice into
    // this response is so a client's own picker never has to.
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
 * One column of a table the builder describes — `columnDefinitionView`'s own
 * wire shape, the same in both directions a `GET` reads and a `PUT` sends
 * (`definition.go`'s own doc on why `provisioning.ColumnDefinition` needs no
 * separate request/response pair).
 *
 * `type` stays a plain `string`, not a `z.enum` over some literal list: the
 * closed set of valid types is `builderLimitsSchema`'s own `columnTypes`,
 * read from the server, and an enum written here would be the exact second
 * copy rule 11 forbids. Whatever a picker cannot find in `columnTypes` it
 * must refuse to offer; a value already saved that is not in that list is
 * not this schema's problem to catch, since the server, not this parse, is
 * what actually validates a column's type.
 */
export const columnDefinitionSchema = z
  .object({
    name: z.string(),
    type: z.string(),
    // Absent, not merely false, on the wire for a NOT NULL column
    // (`nullable,omitempty`) — `.default(false)` reads that the same way an
    // explicit `false` would, which is `ColumnDefinition`'s own stricter
    // default (`definition.go`: "False is the stricter default").
    nullable: z.boolean().default(false),
  })
  .transform((raw) => ({ name: raw.name, type: raw.type, nullable: raw.nullable }));

export type ColumnDefinition = z.infer<typeof columnDefinitionSchema>;

/** One table the builder describes — `tableDefinitionView`'s own wire shape. */
export const tableDefinitionSchema = z
  .object({
    name: z.string(),
    columns: z.array(columnDefinitionSchema).default([]),
    primary_key: z.array(z.string()).default([]),
  })
  .transform((raw) => ({ name: raw.name, columns: raw.columns, primaryKey: raw.primary_key }));

export type TableDefinition = z.infer<typeof tableDefinitionSchema>;

/**
 * What `GET` and a successful `PUT .../game/definition` both answer —
 * `definitionResponse`'s own wire shape. Read and written whole, one request
 * either way: the document is bounded at `builderLimits.maxDefinitionBytes`
 * (tens of kilobytes at the very most), nothing like a dump's own gigabytes,
 * so there is no chunked path here the way `uploadSchema`'s own family needs.
 */
export const definitionSchema = z
  .object({
    tables: z.array(tableDefinitionSchema).default([]),
    builder_limits: builderLimitsSchema,
  })
  .transform((raw) => ({ tables: raw.tables, builderLimits: raw.builder_limits }));

export type GameDefinition = z.infer<typeof definitionSchema>;

/**
 * Where one table's own CSV file has got to —
 * `provisioning.TableDataStatus`'s own three, plus `"absent"`: the same
 * handler-only sentinel `UPLOAD_STATUSES` carries for a dump, sent only by
 * `GET .../tables/{table}/data/current` for a table with nothing 'receiving'
 * — never a status a real upload passes through.
 */
export const TABLE_DATA_STATUSES = ["absent", "receiving", "complete", "aborted"] as const;

export type TableDataStatus = (typeof TABLE_DATA_STATUSES)[number];

/**
 * One table's own chunked CSV upload, or the data it left behind once
 * complete — `tableDataResponse`'s own wire shape, the exact counterpart of
 * `uploadSchema` for a whole dump. `deletedRows` is the tombstoned row
 * numbers themselves (bounded at `builderLimits.maxDeletedRows`), not a
 * count — `activeRows` is `lines` minus how many of those there are, already
 * computed server-side (`TableData.ActiveRows()`), which is the number an
 * organiser's own screen shows as "N rows".
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

/** One row of a table's current data — `tableRowResponse`'s own wire shape. */
export const tableRowSchema = z
  .object({ row: z.number(), fields: z.array(z.string()).default([]) })
  .transform((raw) => ({ row: raw.row, fields: raw.fields }));

export type TableRow = z.infer<typeof tableRowSchema>;

/**
 * A page of one table's current rows — `tableRowWindowResponse`'s own wire
 * shape, the console's own preview of a table before it is ever built, the
 * same role `uploadWindowSchema` plays for a dump's lines. Never the whole
 * table: `rows` is bounded server-side the same way a dump's own window is.
 */
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
