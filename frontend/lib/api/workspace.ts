import { z } from "zod";

import { request, type RequestOptions } from "./client";

/**
 * The participant's own workspace on the play screen: notes and SQL editor
 * tabs, kept on the server per registration
 * (docs/ARCHITECTURE.md §6.4).
 *
 * Two halves, one file, so the whole contract is read in one place:
 *
 * - the wire shapes, parsed on the server by `page.tsx` for the first read
 *   (GET .../play/workspace, which spends the read budget like the rest of
 *   the screen's first load);
 * - the writes, sent by the browser straight to `API_PREFIX` with the session
 *   cookie — the same origin the CSV link and the events stream already use.
 *   They are browser-side rather than server actions because the last save
 *   has to leave as the page is closed, which only a `keepalive` fetch does,
 *   and because Next runs a page's server actions one at a time: an autosave
 *   queued behind a running query (or the other way round) would make each
 *   wait for the other. One request builder serves both the ordinary save
 *   and the one sent on the way out; `keepalive` is the only difference.
 *
 * Every write can be refused with 409 `contest_not_running` or
 * `contest_finished` once the contest has closed for this participant, and
 * with 429 `workspace_too_often` (with `Retry-After`) past sixty writes a
 * minute; the autosave engine reads both from the `ApiError` these raise.
 *
 * zod is imported here although the writes run in the browser: the play
 * route's client bundle already carries it (`components/product/standings.tsx`
 * reads `lib/api/leaderboard.ts`), so parsing a write's answer costs nothing
 * extra there.
 */

/** How many characters the notes may hold (`workspace.MaxNotesRunes`). */
export const NOTES_MAX_CHARS = 20000;

/** From how many characters the notes field shows its counter (§6). */
export const NOTES_COUNTER_FROM = 18000;

/** How many SQL tabs one participant may keep (`workspace.MaxTabs`). */
export const MAX_TABS = 10;

/** How long a tab's title may be, in characters (`workspace.MaxTitleRunes`). */
export const TAB_TITLE_MAX_CHARS = 40;

/** How long a tab's text may be, in UTF-8 bytes (`sqlpolicy.MaxQueryBytes`). */
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
 * GET .../play/workspace. The server creates the first tab when there is
 * none, so `tabs` is never empty on a successful read. `notes.updatedAt` is
 * null until the notes are first saved.
 *
 * `updatedAt` is kept as the server's own string rather than a `Date`: the
 * autosave compares it for equality with the version a draft was written
 * against, and the string is that version exactly.
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

/** The answer to a write that changed one document. */
export const updatedSchema = z
  .object({ updated_at: z.string() })
  .transform((raw) => ({ updatedAt: raw.updated_at }));

export type Updated = z.infer<typeof updatedSchema>;

/** What a save may ask of the transport. */
export type WriteOptions = {
  /** Send so that the request survives the page being hidden or closed. */
  keepalive?: boolean;
};

/**
 * The one browser-side request builder: same origin, session cookie, and
 * keepalive when the caller asks for it. `request` supplies the prefix, the
 * JSON body and the error envelope.
 */
function browserRequest(path: string, options: Omit<RequestOptions, "origin" | "credentials">) {
  return request(path, { ...options, credentials: "same-origin" });
}

function playPath(contestId: string, rest: string): string {
  return `/contests/${encodeURIComponent(contestId)}/play/${rest}`;
}

/** PUT .../play/notes. */
export async function saveNotes(contestId: string, body: string, options: WriteOptions = {}): Promise<Updated> {
  const payload = await browserRequest(playPath(contestId, "notes"), {
    method: "PUT",
    body: { body },
    keepalive: options.keepalive ?? false,
  });
  return updatedSchema.parse(payload);
}

/** POST .../play/tabs. Without a title the body is `{}` and the server names the tab. */
export async function createTab(contestId: string, title?: string): Promise<WorkspaceTab> {
  const payload = await browserRequest(playPath(contestId, "tabs"), {
    method: "POST",
    body: title === undefined ? {} : { title },
  });
  return workspaceTabSchema.parse(payload);
}

/** PATCH .../play/tabs/{tabId}: an absent field is left as it is. */
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

/** DELETE .../play/tabs/{tabId}. The last tab cannot be deleted (409 `workspace_last_tab`). */
export async function deleteTab(contestId: string, tabId: string): Promise<void> {
  await browserRequest(playPath(contestId, `tabs/${encodeURIComponent(tabId)}`), { method: "DELETE" });
}

/** PUT .../play/tabs/order: every tab of the workspace, in the new order. */
export async function reorderTabs(contestId: string, ids: string[]): Promise<void> {
  await browserRequest(playPath(contestId, "tabs/order"), { method: "PUT", body: { ids } });
}

/**
 * One signal the play screen reports about its own participant's browser
 * (design §2.2; `monitor.KindPageLeft` and `monitor.KindPaste`). `client_at`
 * is the browser's own clock, which the server keeps only as a claim.
 */
export type Signal =
  | { kind: "page_left"; client_at: string; away_ms: number }
  | { kind: "paste"; client_at: string; target: PasteTarget; chars: number; text: string };

/** Where a paste is watched (`monitor.PasteTarget`). */
export type PasteTarget = "editor" | "answer" | "notes";

/**
 * POST .../play/signals: a batch of at most 50 signals. Answers 204 even when
 * the server dropped some of them. Refused with 429 `signals_too_often` (with
 * `Retry-After`) past twelve batches a minute, 400 `signals_batch_too_large`,
 * and 409 `contest_not_running` / `contest_finished` once the contest has
 * closed for this participant. Sent through the same browser request builder
 * as the workspace's writes, and with `keepalive` on the way out for the same
 * reason.
 */
export async function sendSignals(contestId: string, events: Signal[], options: WriteOptions = {}): Promise<void> {
  await browserRequest(playPath(contestId, "signals"), {
    method: "POST",
    body: { events },
    keepalive: options.keepalive ?? false,
  });
}
