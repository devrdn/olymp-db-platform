import { NotFoundView } from "@/components/product/not-found-view";
import { activeDictionary } from "@/lib/i18n/server";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.screens.notFound.title };
}

/**
 * A wrong address inside this section. Bare, because the group's layout
 * already renders the shell; the root `not-found.tsx` would add a second one.
 */
export default function NotFound() {
  return <NotFoundView />;
}
