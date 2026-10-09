import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { ContestRegister } from "./contest-register";
import { contestListSchema } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.contests.heading };
}

/** The contest register. A Server Component; filters live in the URL. */
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
  // Ask for contest text in the interface's language, so a page never mixes
  // two.
  search.set("lang", locale);

  // proxy.ts only saw a cookie; a dead session goes back to sign-in, which a
  // retry could never fix.
  const payload = await serverRequest(`/contests?${search}`).catch((error: unknown) => {
    const resume = new URLSearchParams();
    if (query) resume.set("q", query);
    if (status) resume.set("status", status);
    const here = resume.size > 0 ? `/contests?${resume}` : "/contests";

    const target = authRecoveryRedirect(error, here);
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
