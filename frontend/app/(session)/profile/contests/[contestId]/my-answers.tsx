"use client";

import { AnswerAttempts } from "@/components/product/answer-attempts";
import type { Answers } from "@/lib/api/journal";

import type { ReportDict } from "./report-tabs";

/**
 * Every attempt this participant made, by question, each opening to the
 * queries that led to it — the same view the organiser has of the same
 * record, in the second person and without the address column.
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
