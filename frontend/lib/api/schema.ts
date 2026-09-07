import { z } from "zod";

/**
 * The shape of a contest's game database — GET .../play/schema.
 *
 * What the console's schema panel draws, and deliberately not the catalogue:
 * tables, their columns, each column's type and the table a foreign key
 * points at. Everything else about how the game is stored is either
 * something a participant can ask the catalogue for themselves, in a contest
 * that leaves it open, or something the contest is hiding on purpose.
 *
 * The endpoint refuses outright (`schema_hidden`) in a contest whose
 * organiser closed the catalogues, which is why the panel is optional
 * everywhere it appears rather than empty.
 */
export const gameColumnSchema = z.object({
  name: z.string(),
  type: z.string(),
  nullable: z.boolean(),
  /** The table a foreign key points at; empty when the column points at nothing. */
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
  /** The game has more than the panel is being shown. */
  truncated: z.boolean(),
});

export type GameSchema = z.infer<typeof gameSchemaSchema>;
