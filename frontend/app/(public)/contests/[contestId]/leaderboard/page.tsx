import { notFound } from "next/navigation";

import { Band } from "@/components/layout/band";
import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { standingsSchema, type Standings } from "@/lib/api/leaderboard";
import { serverRequest } from "@/lib/api/server";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { PublicStandings } from "./public-standings";
import { messageForCode } from "@/lib/i18n/errors";

type Loaded = { kind: "ok"; standings: Standings } | { kind: "refused"; code: string };

async function load(contestId: string, locale: string): Promise<Loaded> {
  if (!isId(contestId)) notFound();
  try {
    const payload = await serverRequest(`/contests/${contestId}/leaderboard?lang=${locale}`);
    return { kind: "ok", standings: standingsSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError && error.status === 404) notFound();
    if (error instanceof ApiError && error.status === 429) return { kind: "refused", code: error.code };
    throw error;
  }
}

/**
 * A contest's table, open to anybody with the link: participants, the
 * audience in the hall, whoever the organiser shares it with.
 *
 * Kept out of search engines on two levels — this metadata, and the API's own
 * `X-Robots-Tag` — because it is a page of people's names.
 */
export async function generateMetadata(props: PageProps<"/contests/[contestId]/leaderboard">) {
  const [{ contestId }, dict, locale] = await Promise.all([props.params, activeDictionary(), activeLocale()]);
  const loaded = await load(contestId, locale);
  const title = loaded.kind === "ok" && loaded.standings.title ? loaded.standings.title : "";
  return {
    title: title ? `${dict.leaderboard.publicTitle} · ${title}` : dict.leaderboard.publicTitle,
    robots: { index: false, follow: false },
  };
}

export default async function PublicLeaderboardPage(props: PageProps<"/contests/[contestId]/leaderboard">) {
  const [{ contestId }, dict, locale] = await Promise.all([props.params, activeDictionary(), activeLocale()]);
  const loaded = await load(contestId, locale);

  return (
    <Band fill className="gap-8">
      <header className="flex flex-col gap-3">
        <p className="font-mono text-label text-ink-3 uppercase">{dict.leaderboard.publicTitle}</p>
        {loaded.kind === "ok" ? (
          <h1 className="max-w-body text-h2 text-ink">{loaded.standings.title}</h1>
        ) : null}
      </header>

      {loaded.kind === "ok" ? (
        <PublicStandings
          contestId={contestId}
          initial={loaded.standings}
          // Only the table's own vocabulary crosses to the client.
          dict={{ leaderboard: dict.leaderboard }}
          locale={locale}
        />
      ) : (
        <p role="status" className="text-body text-warn">
          {messageForCode(loaded.code, dict.errors)}
        </p>
      )}
    </Band>
  );
}
