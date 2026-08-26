import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { ContestRegister } from "./contest-register";
import { contestListSchema } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { expiredSessionRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

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

  // The proxy could only see that a session cookie exists; whether it is still
  // worth anything is this answer. A dead one goes back to the form rather than
  // to the recoverable-error screen, whose retry could never fix it.
  const payload = await serverRequest(`/contests?${search}`).catch((error: unknown) => {
    const resume = new URLSearchParams();
    if (query) resume.set("q", query);
    if (status) resume.set("status", status);
    const here = resume.size > 0 ? `/contests?${resume}` : "/contests";

    const target = expiredSessionRedirect(error, here);
    if (target) redirect(target);
    throw error;
  });

  const { items, total } = contestListSchema.parse(payload);

  return (
    <Band fill className="py-12">
      <ContestRegister
        contests={items}
        total={total}
        dict={dict}
        locale={locale}
        filtered={Boolean(query || status)}
        resetHref="/contests"
      />
    </Band>
  );
}
