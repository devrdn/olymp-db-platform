import { NotFoundView } from "@/components/product/not-found-view";
import { activeDictionary } from "@/lib/i18n/server";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.screens.notFound.title };
}

/**
 * Not-found inside this group, bare because the layout already renders the
 * shell; the root one would stack a second header.
 */
export default function NotFound() {
  return <NotFoundView />;
}
