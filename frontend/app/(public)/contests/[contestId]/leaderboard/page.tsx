import { notFound } from "next/navigation";

import { Band } from "@/components/layout/band";
import { isId } from "@/lib/api/ids";
import { standingsSchema } from "@/lib/api/leaderboard";
import { readPage, Refusal } from "@/lib/api/page-read";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { PublicStandings } from "@/components/product/public-standings";
import { RefusalNote } from "@/components/product/refusal-note";

/** The table, or the rate limit's refusal; the metadata and the page share the request. */
async function load(contestId: string, locale: string) {
  if (!isId(contestId)) notFound();
  return readPage(`/contests/${contestId}/leaderboard?lang=${locale}`, (payload) => standingsSchema.parse(payload), {
    here: `/contests/${contestId}/leaderboard`,
    shown: ["leaderboard_too_often"],
  });
}

/**
 * A contest's table for anyone with the link. Kept out of search engines by
 * this metadata and the API's `X-Robots-Tag`, since it lists people's names.
 */
export async function generateMetadata(props: PageProps<"/contests/[contestId]/leaderboard">) {
  const [{ contestId }, dict, locale] = await Promise.all([props.params, activeDictionary(), activeLocale()]);
  const loaded = await load(contestId, locale);
  const title = loaded instanceof Refusal ? "" : loaded.title;
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
        {loaded instanceof Refusal ? null : <h1 className="max-w-body text-h2 text-ink">{loaded.title}</h1>}
      </header>

      {loaded instanceof Refusal ? (
        <RefusalNote code={loaded.code} errors={dict.errors} />
      ) : (
        <PublicStandings
          contestId={contestId}
          initial={loaded}
          // Only the table's vocabulary crosses to the client.
          dict={{ leaderboard: dict.leaderboard }}
          locale={locale}
        />
      )}
    </Band>
  );
}
