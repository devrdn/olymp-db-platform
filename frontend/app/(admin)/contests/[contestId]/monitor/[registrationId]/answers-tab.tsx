"use client";

import { AnswerAttempts } from "@/components/product/answer-attempts";
import type { Answers } from "@/lib/api/monitor";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * The answers tab (SPEC.md §5.1): `AnswerAttempts` with staff wording and each
 * query's address.
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
