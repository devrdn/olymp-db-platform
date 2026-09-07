import { NotFoundView } from "@/components/product/not-found-view";
import { activeDictionary } from "@/lib/i18n/server";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.screens.notFound.title };
}

/**
 * A wrong address inside this section.
 *
 * Bare, because this group's layout already renders the shell. Without this
 * file the root `not-found.tsx` answered instead and brought a second one
 * with it — two headers stacked on one screen.
 */
export default function NotFound() {
  return <NotFoundView />;
}
