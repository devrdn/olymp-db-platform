import { NotFoundView } from "@/components/product/not-found-view";
import { activeDictionary } from "@/lib/i18n/server";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.screens.notFound.title };
}

/**
 * Also what the archive looks like while it is switched off: a section that
 * is not there has no page to find, and says no more than a wrong address.
 */
export default function NotFound() {
  return <NotFoundView />;
}
