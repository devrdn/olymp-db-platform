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
