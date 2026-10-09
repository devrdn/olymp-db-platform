import { z } from "zod";

/**
 * The game database as the console's schema panel draws it (`.../play/schema`):
 * tables, columns, types and foreign-key targets, not the catalogue. Refused
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

export const gameTableSchema = z.object({
  name: z.string(),
  columns: z.array(gameColumnSchema),
});

export type GameTable = z.infer<typeof gameTableSchema>;

export const gameSchemaSchema = z.object({
  tables: z.array(gameTableSchema),
  truncated: z.boolean(),
});

export type GameSchema = z.infer<typeof gameSchemaSchema>;
