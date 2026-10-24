import { z } from "zod";

/**
 * The game database as the console's schema panel and diagram draw it
 * (`.../play/schema`): tables, columns, types and keys, not the catalogue. Refused
 * with `schema_hidden` when the organiser closed the catalogues, so the panel
 * is optional wherever it appears.
 */

/** One column of a game table. */
export const gameColumnSchema = z.object({
  name: z.string(),
  type: z.string(),
  nullable: z.boolean(),
  /** The table a foreign key points at; empty when none. */
  references: z.string(),
});

export type GameColumn = z.infer<typeof gameColumnSchema>;

/**
 * One foreign key whose referenced table is in the game's schema. `columns`
 * and `referenced_columns` pair up by position, in the constraint's order.
 */
export const gameForeignKeySchema = z.object({
  name: z.string(),
  columns: z.array(z.string()),
  referenced_table: z.string(),
  referenced_columns: z.array(z.string()),
});

export type GameForeignKey = z.infer<typeof gameForeignKeySchema>;

export const gameTableSchema = z.object({
  name: z.string(),
  columns: z.array(gameColumnSchema),
  /**
   * The primary key's columns in key order. Both key lists are optional
   * because an API deployed before keys were described omits them; read an
   * absent list as empty.
   */
  primary_key: z.array(z.string()).optional(),
  foreign_keys: z.array(gameForeignKeySchema).optional(),
});

export type GameTable = z.infer<typeof gameTableSchema>;

export const gameSchemaSchema = z.object({
  tables: z.array(gameTableSchema),
  truncated: z.boolean(),
});

export type GameSchema = z.infer<typeof gameSchemaSchema>;
