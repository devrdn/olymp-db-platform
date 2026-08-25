import { contestListSchema } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { ContestRegister } from "@/components/product/contest-register";
import { LanguageSwitcher } from "@/components/layout/language-switcher";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.contests.heading };
}

/**
 * The constructor's index.
 *
 * A Server Component: the data is fetched where the session already is, so no
 * token reaches the browser and the first paint carries the rows. Filters live
 * in the URL, which makes them shareable and the reset a plain link.
 */
export default async function ContestsPage(props: PageProps<"/contests">) {
  const [params, locale, dict] = await Promise.all([
    props.searchParams,
    activeLocale(),
    activeDictionary(),
  ]);

  const query = typeof params.q === "string" ? params.q : "";
  const status = typeof params.status === "string" ? params.status : "";

  const search = new URLSearchParams();
  if (query) search.set("q", query);
  if (status) search.set("status", status);
  // The server negotiates the contest's own text; ask it in the same language
  // the interface is rendering, so a page never mixes two.
  search.set("lang", locale);

  const payload = await serverRequest(`/contests?${search}`);
  const { items, total } = contestListSchema.parse(payload);

  return (
    <main className="mx-auto w-full max-w-6xl px-4 py-10 sm:px-6 lg:px-8">
      <div className="flex justify-end pb-4">
        <LanguageSwitcher current={locale} />
      </div>
      <ContestRegister
        contests={items}
        total={total}
        dict={dict}
        locale={locale}
        filtered={Boolean(query || status)}
        resetHref="/contests"
      />
    </main>
  );
}
