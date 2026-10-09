"use client";

import { AnswerAttempts } from "@/components/product/answer-attempts";
import type { Answers } from "@/lib/api/journal";

import type { ReportDict } from "./report-tabs";

/**
 * The participant's attempts by question with the queries behind each, as
 * organisers see them but without the address column.
 */
export function MyAnswers({
  answers,
  t,
  statuses,
  locale,
}: {
  answers: Answers;
  t: ReportDict;
  statuses: Record<string, string>;
  locale: string;
}) {
  return (
    <AnswerAttempts
      answers={answers}
      labels={t.answers}
      queryLabels={t.queries}
      statuses={statuses}
      locale={locale}
    />
  );
}
