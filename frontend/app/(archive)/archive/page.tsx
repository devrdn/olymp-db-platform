import Link from "next/link";
import { notFound, redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { ContestWindow } from "@/components/product/contest-window";
import { StateView } from "@/components/product/state-view";
import { buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollBox } from "@/components/ui/scroll-box";
import { Tag } from "@/components/ui/tag";
import { ARCHIVE_PAGE_SIZE, ARCHIVE_QUERY_MAX, archivePageSchema, archiveSearch } from "@/lib/api/archive";
import { branding } from "@/lib/api/branding";
import { ApiError } from "@/lib/api/client";
import { CONTEST_STATUS_TONE } from "@/lib/api/contests-terms";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { messageForCode } from "@/lib/i18n/errors";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { plural } from "@/lib/i18n/plural";
import { fill } from "@/lib/i18n/fill";
import { cn } from "@/lib/utils";

const HEAD = "border-b border-line-2 px-(--row-px) py-2.5 font-mono text-label font-medium text-ink-3 uppercase";
const CELL = "border-b border-line px-(--row-px) py-(--row-py) align-baseline";

export async function generateMetadata() {
  const dict = await activeDictionary();
  // The list names people's contests and nothing a search engine should hold.
  return { title: dict.archive.heading, robots: { index: false, follow: false } };
}

/**
 * Contests already held. A Server Component with no client state: the search
 * is a GET form and paging is links, so a view is an address that can be
 * shared and works without JavaScript.
 *
 * Below `wide` the window and the participants fold into a caption under the
 * title, and on a phone the status follows them (SPEC.md §5).
 */
export default async function ArchivePage(props: PageProps<"/archive">) {
  const [params, brand, dict, locale] = await Promise.all([
    props.searchParams,
    branding(),
    activeDictionary(),
    activeLocale(),
  ]);
  // Before any request: an archive that is off must not cost the API a read.
  if (!brand.archive) notFound();

  const text = typeof params.q === "string" ? params.q : "";
  const page = typeof params.page === "string" ? Number(params.page) : undefined;
  const search = archiveSearch(text, page, locale);
  const q = search.get("q") ?? "";
  const offset = Number(search.get("offset"));
  const current = offset / ARCHIVE_PAGE_SIZE + 1;

  const hrefFor = (number: number) => {
    const shown = new URLSearchParams();
    if (q) shown.set("q", q);
    shown.set("page", String(number));
    return `/archive?${shown}`;
  };

  const t = dict.archive;
  let loaded;
  try {
    loaded = archivePageSchema.parse(await serverRequest(`/archive/contests?${search}`));
  } catch (error: unknown) {
    // proxy.ts only saw a cookie; this answer says whether the session is alive.
    const target = authRecoveryRedirect(error, q ? `/archive?${new URLSearchParams({ q })}` : "/archive");
    if (target) redirect(target);
    // The switch was turned off since the branding was read.
    if (error instanceof ApiError && error.status === 404) notFound();
    if (error instanceof ApiError && error.status === 429) {
      return (
        <Band fill className="py-12">
          <h1 className="text-h2 text-ink">{t.heading}</h1>
          <p role="status" className="pt-6 text-body text-warn">
            {messageForCode(error.code, dict.errors)}
          </p>
        </Band>
      );
    }
    throw error;
  }

  const { items, total } = loaded;
  const hasPrevious = current > 1;
  const hasNext = offset + items.length < total;

  return (
    <Band fill className="flex flex-col gap-8 py-12">
      <div className="flex flex-col gap-2">
        <h1 className="text-h2 text-ink">{t.heading}</h1>
        <p className="max-w-prose text-body text-ink-2">{t.lede}</p>
      </div>

      <form method="get" action="/archive" role="search" className="flex flex-wrap items-end gap-3">
        <div className="flex w-full max-w-md min-w-0 flex-col gap-1.5">
          <Label htmlFor="archive-q">{t.search.label}</Label>
          <Input id="archive-q" type="search" name="q" defaultValue={q} maxLength={ARCHIVE_QUERY_MAX} />
        </div>
        <button type="submit" className={cn(buttonVariants({ variant: "secondary" }))}>
          {t.search.submit}
        </button>
      </form>

      {items.length === 0 ? (
        <div className="border-t border-line">
          {total > 0 ? (
            <StateView
              state={{
                kind: "empty-filtered",
                title: t.pastEnd.title,
                body: t.pastEnd.body,
                reset: { label: t.pastEnd.first, href: hrefFor(1) },
              }}
            />
          ) : q ? (
            <StateView
              state={{
                kind: "empty-filtered",
                title: t.noMatch.title,
                body: t.noMatch.body,
                reset: { label: t.search.reset, href: "/archive" },
              }}
            />
          ) : (
            <StateView state={{ kind: "empty", title: t.empty.title, body: t.empty.body }} />
          )}
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          <ScrollBox>
            <table className="w-full border-collapse text-left max-narrow:[--row-px:--spacing(2)] wide:min-w-3xl">
              <thead>
                <tr>
                  <th scope="col" className={HEAD}>
                    {t.columns.title}
                  </th>
                  <th scope="col" className={cn(HEAD, "w-52 max-wide:hidden")}>
                    {t.columns.window}
                  </th>
                  <th scope="col" className={cn(HEAD, "w-36 max-narrow:hidden")}>
                    {t.columns.status}
                  </th>
                  <th scope="col" className={cn(HEAD, "w-40 max-wide:hidden")}>
                    {t.columns.participants}
                  </th>
                  <th scope="col" className={cn(HEAD, "w-32 max-narrow:w-px")}>
                    {t.columns.results}
                  </th>
                </tr>
              </thead>
              <tbody>
                {/* No prefetch: each would spend the contest page's reads from the reader's budget. */}
                {items.map((contest) => (
                  <tr key={contest.id} className="transition-colors duration-(--t-input) ease-standard hover:bg-panel">
                    <td className={CELL}>
                      <Link
                        href={`/archive/${contest.id}`}
                        prefetch={false}
                        className="text-row text-ink underline-offset-4 hover:underline"
                      >
                        {contest.title}
                      </Link>
                      <span className="mt-2 hidden max-narrow:block">
                        <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{dict.contests.status[contest.status]}</Tag>
                      </span>
                      <span data-caption className="mt-2 hidden font-mono text-data text-ink-2 max-wide:block">
                        <ContestWindow
                          startsAt={contest.startsAt}
                          endsAt={contest.endsAt}
                          locale={locale}
                          unscheduled={dict.contests.unscheduled}
                          until={dict.contests.until}
                        />
                        <span className="block text-ink-3">{plural(t.participants, contest.participants, locale)}</span>
                      </span>
                    </td>
                    <td className={cn(CELL, "font-mono text-data whitespace-nowrap text-ink-2 max-wide:hidden")}>
                      <ContestWindow
                        startsAt={contest.startsAt}
                        endsAt={contest.endsAt}
                        locale={locale}
                        unscheduled={dict.contests.unscheduled}
                        until={dict.contests.until}
                      />
                    </td>
                    <td className={cn(CELL, "max-narrow:hidden")}>
                      <Tag tone={CONTEST_STATUS_TONE[contest.status]}>{dict.contests.status[contest.status]}</Tag>
                    </td>
                    <td className={cn(CELL, "font-mono text-data whitespace-nowrap text-ink-2 max-wide:hidden")}>
                      {plural(t.participants, contest.participants, locale)}
                    </td>
                    <td className={CELL}>
                      <Link
                        href={`/archive/${contest.id}#results`}
                        prefetch={false}
                        aria-label={fill(t.resultsOf, { title: contest.title })}
                        className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
                      >
                        {t.columns.results}
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollBox>

          <nav aria-label={t.heading} className="flex flex-wrap items-center justify-between gap-3">
            <span className="font-mono text-data text-ink-3">
              {fill(t.pagination.range, { from: offset + 1, to: offset + items.length, total })}
            </span>
            <span className="flex gap-2">
              {hasPrevious ? (
                <Link href={hrefFor(current - 1)} className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}>
                  {t.pagination.previous}
                </Link>
              ) : null}
              {hasNext ? (
                <Link href={hrefFor(current + 1)} className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}>
                  {t.pagination.next}
                </Link>
              ) : null}
            </span>
          </nav>
        </div>
      )}
    </Band>
  );
}
