import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { activeDictionary } from "@/lib/i18n/server";

/**
 * The not-found message without a shell: each route group already renders one,
 * and a second would stack a header. The root `not-found.tsx` wraps it because
 * no group's bar applies there. Offers a way out, not a retry.
 */
export async function NotFoundView() {
  const dict = await activeDictionary();
  const t = dict.screens.notFound;

  return (
    <Band fill>
      <StateView
        state={{
          kind: "error-terminal",
          title: t.title,
          body: t.body,
          exit: { label: t.home, href: "/contests" },
        }}
      />
    </Band>
  );
}
