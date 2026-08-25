import { contestListSchema } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { ContestRegister } from "@/components/product/contest-register";

export const metadata = { title: "Олимпиады" };

/**
 * The constructor's index.
 *
 * A Server Component: the data is fetched where the session already is, so no
 * token reaches the browser and the first paint carries the rows. Filters live
 * in the URL, which makes them shareable and the reset a plain link.
 */
export default async function ContestsPage(props: PageProps<"/contests">) {
  const params = await props.searchParams;
  const query = typeof params.q === "string" ? params.q : "";
  const status = typeof params.status === "string" ? params.status : "";

  const search = new URLSearchParams();
  if (query) search.set("q", query);
  if (status) search.set("status", status);
  const suffix = search.size > 0 ? `?${search}` : "";

  const payload = await serverRequest(`/contests${suffix}`);
  const { items, total } = contestListSchema.parse(payload);

  return (
    <main className="mx-auto w-full max-w-6xl px-4 py-10 sm:px-6 lg:px-8">
      <ContestRegister
        contests={items}
        total={total}
        filtered={Boolean(query || status)}
        resetHref="/contests"
      />
    </main>
  );
}
