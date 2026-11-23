import { Frame } from "@/components/layout/frame";
import { NotFoundView } from "@/components/product/not-found-view";
import { activeDictionary } from "@/lib/i18n/server";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.screens.notFound.title };
}

/**
 * Not-found for addresses outside every route group; the only one with a frame,
 * since no group layout supplies one.
 */
export default function NotFound() {
  return (
    <Frame audience="public">
      <NotFoundView />
    </Frame>
  );
}
