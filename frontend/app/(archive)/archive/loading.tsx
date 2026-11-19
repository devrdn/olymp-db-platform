import { Band } from "@/components/layout/band";
import { Skeleton } from "@/components/ui/skeleton";
import { activeDictionary } from "@/lib/i18n/server";

/** Shaped like the list, with its search row and rows of the real height, so nothing moves when it lands. */
export default async function Loading() {
  const dict = await activeDictionary();

  return (
    <Band fill className="py-12">
      <div className="flex flex-col gap-3 pb-6">
        <Skeleton className="h-8 w-40 rounded-none" />
        <Skeleton className="h-4 w-80 max-w-full" />
      </div>
      <Skeleton className="mb-6 h-(--control-h) w-full max-w-md rounded-none" />

      <div aria-hidden className="border-t border-line-2">
        {[0, 1, 2, 3].map((row) => (
          <div key={row} className="flex items-start gap-3.5 border-b border-line px-3.5 py-3.5">
            <div className="flex min-w-0 flex-1 flex-col gap-2">
              <Skeleton className="h-3.5 w-56 max-w-full" />
              <Skeleton className="h-3 w-40 max-w-full" />
            </div>
            <Skeleton className="mt-0.5 hidden h-4 w-20 shrink-0 narrow:block" />
            <Skeleton className="h-7 w-20 shrink-0 rounded-full" />
          </div>
        ))}
      </div>

      <span role="status" className="sr-only">
        {dict.archive.loading}
      </span>
    </Band>
  );
}
