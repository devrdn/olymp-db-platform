import { z } from "zod";

import { request, type RequestOptions } from "./client";

/**
 * The participant's workspace on the play screen: notes and SQL tabs, kept on
 * the server per registration (docs/ARCHITECTURE.md §6.4). The first read is
 * parsed server-side by `page.tsx`.
 *
 * Writes go from the browser straight to the API with the session cookie, not
 * through server actions: the last save must leave as the page closes, which
 * only a `keepalive` fetch does, and Next runs a page's server actions one at
 * a time, so an autosave and a query would wait on each other.
 *
 * Every write may be refused with 409 `contest_ended`, `contest_finished` or
 * `deadline_passed`, or 429 `workspace_too_often` (with `Retry-After`) past
 * sixty a minute.
 *
 * zod costs nothing extra here: the play route's client bundle already
 * carries it (`components/product/standings.tsx` reads
 * `lib/api/leaderboard.ts`).
 */

/** The most characters the notes may hold (`workspace.MaxNotesRunes`). */
export const NOTES_MAX_CHARS = 20000;

/** The length at which the notes field starts showing its counter. */
export const NOTES_COUNTER_FROM = 18000;

/** The most SQL tabs one participant may keep (`workspace.MaxTabs`). */
export const MAX_TABS = 10;

/** The most characters a tab's title may hold (`workspace.MaxTitleRunes`). */
export const TAB_TITLE_MAX_CHARS = 40;

/** The most UTF-8 bytes a tab's text may hold (`sqlpolicy.MaxQueryBytes`). */
export const TAB_BODY_MAX_BYTES = 64 * 1024;

export const workspaceTabSchema = z
  .object({
    id: z.string().min(1),
    title: z.string(),
    body: z.string(),
    position: z.number().int(),
    updated_at: z.string(),
  })
  .transform((raw) => ({
    id: raw.id,
    title: raw.title,
    body: raw.body,
    position: raw.position,
    updatedAt: raw.updated_at,
  }));

export type WorkspaceTab = z.infer<typeof workspaceTabSchema>;

/**
 * The workspace as GET …/play/workspace returns it: `tabs` is never empty,
 * since the server creates the first. `notes.updatedAt` is null until the
 * first save. `updatedAt` stays the server's string, not a `Date`, because
 * the autosave compares it for equality as a version.
 */
export const workspaceSchema = z
  .object({
    notes: z.object({ body: z.string(), updated_at: z.string().nullable() }),
    tabs: z.array(workspaceTabSchema),
  })
  .transform((raw) => ({
    notes: { body: raw.notes.body, updatedAt: raw.notes.updated_at },
    tabs: raw.tabs,
  }));

export type WorkspaceSnapshot = z.infer<typeof workspaceSchema>;

export type WorkspaceNotes = WorkspaceSnapshot["notes"];

export const updatedSchema = z
  .object({ updated_at: z.string() })
  .transform((raw) => ({ updatedAt: raw.updated_at }));

export type Updated = z.infer<typeof updatedSchema>;

export type WriteOptions = {
  /** Lets the request survive the page being hidden or closed. */
  keepalive?: boolean;
};

function browserRequest(path: string, options: Omit<RequestOptions, "origin" | "credentials">) {
  return request(path, { ...options, credentials: "same-origin" });
}

function playPath(contestId: string, rest: string): string {
  return `/contests/${encodeURIComponent(contestId)}/play/${rest}`;
}

export async function saveNotes(contestId: string, body: string, options: WriteOptions = {}): Promise<Updated> {
  const payload = await browserRequest(playPath(contestId, "notes"), {
    method: "PUT",
    body: { body },
    keepalive: options.keepalive ?? false,
  });
  return updatedSchema.parse(payload);
}

/** Creates a tab; without a title the server names it. */
export async function createTab(contestId: string, title?: string): Promise<WorkspaceTab> {
  const payload = await browserRequest(playPath(contestId, "tabs"), {
    method: "POST",
    body: title === undefined ? {} : { title },
  });
  return workspaceTabSchema.parse(payload);
}

/** Updates a tab; an absent field is left as it is. */
export async function updateTab(
  contestId: string,
  tabId: string,
  patch: { title?: string; body?: string },
  options: WriteOptions = {},
): Promise<Updated> {
  const payload = await browserRequest(playPath(contestId, `tabs/${encodeURIComponent(tabId)}`), {
    method: "PATCH",
    body: patch,
    keepalive: options.keepalive ?? false,
  });
  return updatedSchema.parse(payload);
}

/** Deletes a tab; the last cannot be (409 `workspace_last_tab`). */
export async function deleteTab(contestId: string, tabId: string): Promise<void> {
  await browserRequest(playPath(contestId, `tabs/${encodeURIComponent(tabId)}`), { method: "DELETE" });
}

/** Saves the tab order; `ids` must name every tab, in the new order. */
export async function reorderTabs(contestId: string, ids: string[]): Promise<void> {
  await browserRequest(playPath(contestId, "tabs/order"), { method: "PUT", body: { ids } });
}

/**
 * A signal the play screen reports about the participant's browser
 * (docs/ARCHITECTURE.md §9.4). `client_at` is the browser's clock, which the server keeps only as a
 * claim.
 */
export type Signal =
  | { kind: "page_left"; client_at: string; away_ms: number }
  | { kind: "paste"; client_at: string; target: PasteTarget; chars: number; text: string };

export type PasteTarget = "editor" | "answer" | "notes";

/**
 * Sends at most 50 signals; 204 even when the server dropped some. Refused
 * with 429 `signals_too_often` past twelve batches a minute, 400
 * `signals_batch_too_large`, or the same 409s as the workspace writes.
 */
export async function sendSignals(contestId: string, events: Signal[], options: WriteOptions = {}): Promise<void> {
  await browserRequest(playPath(contestId, "signals"), {
    method: "POST",
    body: { events },
    keepalive: options.keepalive ?? false,
  });
}
