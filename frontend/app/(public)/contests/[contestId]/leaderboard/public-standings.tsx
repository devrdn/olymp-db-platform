"use client";

import { StandingsView, type StandingsDictionary } from "@/components/product/standings";
import { useStandings } from "@/components/product/use-standings";
import type { Standings } from "@/lib/api/leaderboard";

import { fetchPublicStandingsAction } from "./actions";

/** The public table, kept fresh for as long as the page is open and visible. */
export function PublicStandings({
  contestId,
  initial,
  dict,
  locale,
}: {
  contestId: string;
  initial: Standings;
  dict: StandingsDictionary;
  locale: string;
}) {
  const { standings, failed } = useStandings({
    load: () => fetchPublicStandingsAction(contestId),
    active: true,
    initial,
  });

  return <StandingsView standings={standings ?? initial} dict={dict} locale={locale} variant="page" failed={failed} />;
}
