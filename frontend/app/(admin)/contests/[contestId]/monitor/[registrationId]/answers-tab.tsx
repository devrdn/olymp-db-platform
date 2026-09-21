"use client";

import { AnswerAttempts } from "@/components/product/answer-attempts";
import type { Answers } from "@/lib/api/monitor";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * The answers tab (design §3, §6): every attempt, by question in order, each
 * opening to the queries that led to it.
 *
 * The list is `AnswerAttempts`, shared with the participant's own report; this
 * is the monitoring half of it — the organiser's words, and the address on
 * each query under an attempt.
 */
export function AnswersTab({ answers, dict, locale }: { answers: Answers; dict: Dictionary; locale: string }) {
  const t = dict.workspace.monitor.participant;
  return (
    <AnswerAttempts
      answers={answers}
      labels={t.answers}
      queryLabels={t.queries}
      statuses={dict.workspace.monitor.feed.queryStatus}
      locale={locale}
      address
    />
  );
}
