"use client";

import { StandingsView, type StandingsDictionary } from "@/components/product/standings";
import { useStandings } from "@/components/product/use-standings";
import type { Standings } from "@/lib/api/leaderboard";
import type { Locale } from "@/lib/i18n/config";

import { fetchPublicStandings } from "./public-standings-request";

/**
 * The public table, kept fresh for as long as the page is open and visible.
 * Without an `initial` copy it reads one at once and shows `placeholder` until
 * a read succeeds.
 */
export function PublicStandings({
  contestId,
  initial,
  dict,
  locale,
  placeholder = null,
}: {
  contestId: string;
  initial?: Standings;
  dict: StandingsDictionary;
  locale: Locale;
  placeholder?: React.ReactNode;
}) {
  const { standings, failed } = useStandings({
    load: () => fetchPublicStandings(contestId, locale),
    active: true,
    initial,
  });

  const shown = standings ?? initial;
  if (!shown) return placeholder;
  return <StandingsView standings={shown} dict={dict} locale={locale} variant="page" failed={failed} />;
}
