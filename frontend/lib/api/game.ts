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

export const gameSchema = z
  .object({
    status: z.enum(GAME_STATUSES),
    version: z.number().default(0),
    database: z.string().default(""),
    build_error: z.string().default(""),
    script_bytes: z.number().default(0),
    building: z.boolean().default(false),
    updated_at: z.string().optional(),
  })
  .transform((raw) => ({
    status: raw.status,
    version: raw.version,
    database: raw.database,
    buildError: raw.build_error,
    scriptBytes: raw.script_bytes,
    building: raw.building,
    updatedAt: raw.updated_at ?? "",
  }));

export type Game = z.infer<typeof gameSchema>;

export const gameScriptSchema = z.object({ script: z.string() });
