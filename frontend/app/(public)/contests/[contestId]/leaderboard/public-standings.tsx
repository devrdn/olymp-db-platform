"use client";

import { StandingsView, type StandingsDictionary } from "@/components/product/standings";
import { useStandings } from "@/components/product/use-standings";
import type { Standings } from "@/lib/api/leaderboard";
import type { Locale } from "@/lib/i18n/config";

import { fetchPublicStandings } from "./requests";

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
  locale: Locale;
}) {
  const { standings, failed } = useStandings({
    load: () => fetchPublicStandings(contestId, locale),
    active: true,
    initial,
  });

  return <StandingsView standings={standings ?? initial} dict={dict} locale={locale} variant="page" failed={failed} />;
}
