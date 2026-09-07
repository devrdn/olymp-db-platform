import { Band } from "@/components/layout/band";
import { StateView } from "@/components/product/state-view";
import { activeDictionary } from "@/lib/i18n/server";

/**
 * What an address that leads nowhere says — the words and nothing around
 * them.
 *
 * Apart from the shell on purpose. Every route group already renders one
 * (ProductShell or FocusShell), and a `not-found.tsx` that brought its own
 * put a second header under the first — which is what a wrong address inside
 * the workspace actually looked like. The root `not-found.tsx` is the one
 * place that still wraps this in a shell, because a URL matching no group at
 * all has no other bar to sit under.
 *
 * A terminal error and not a recoverable one: a wrong address does not become
 * right on a second attempt, so it offers a way out instead of a retry.
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
