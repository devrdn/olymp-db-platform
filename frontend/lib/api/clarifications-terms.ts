/** The bounds of questions, answers and announcements, apart from the wire schemas (see content-terms.ts for why). */

import type { FeedItem } from "./clarifications";

/** The most characters a question may hold (`clarifications.MaxQuestionRunes`). */
export const QUESTION_MAX_CHARS = 2000;

/** The most characters an answer may hold (`clarifications.MaxAnswerRunes`). */
export const ANSWER_MAX_CHARS = 4000;

/** The most characters an announcement may hold (`clarifications.MaxAnnouncementRunes`). */
export const ANNOUNCEMENT_MAX_CHARS = 4000;

/** How many of one participant's questions may wait at once (`clarifications.MaxOpenPerRegistration`). */
export const MAX_OPEN_QUESTIONS = 20;

/** One page of the queue or the announcements; the server's `DefaultPageSize`. */
export const CLARIFICATION_PAGE_SIZE = 50;

/** The most of one state the desk keeps live: the server's `MaxPageSize`. */
export const QUEUE_MAX_SHOWN = 100;

/**
 * The newest moment anything in items changed that the reader did not do
 * themselves: an announcement, an answer, an edit. A question of their own
 * still waiting is their own act and never counts as news. Empty when there
 * is nothing.
 */
export function latestStamp(items: FeedItem[]): string {
  let latest = "";
  for (const item of items) {
    if (item.kind === "clarification" && item.mine && item.answer === null) continue;
    const stamps = [item.postedAt];
    if (item.kind === "clarification" && item.answer?.editedAt) stamps.push(item.answer.editedAt);
    for (const stamp of stamps) if (stamp > latest) latest = stamp;
  }
  return latest;
}
