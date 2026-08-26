import { Band } from "@/components/layout/band";
import { Skeleton } from "@/components/ui/skeleton";
import { activeDictionary } from "@/lib/i18n/server";

/**
 * A skeleton shaped like the register, not a spinner: a spinner says "wait", a
 * skeleton says "here is what is coming". Its rows carry the real row height
 * and the real column offsets, so nothing moves when the data lands — the
 * trailing block stands in for the join control, which is the widest thing in
 * its column and therefore the one worth reserving.
 */
export default async function Loading() {
  const dict = await activeDictionary();

  return (
    <Band fill className="py-12">
      <div className="flex items-baseline justify-between gap-6 pb-6">
        <Skeleton className="h-8 w-52 rounded-none" />
        <Skeleton className="h-3 w-20" />
      </div>

      <div aria-hidden className="border-t border-line-2">
        {[0, 1, 2, 3].map((row) => (
          <div key={row} className="flex items-start gap-3.5 border-b border-line px-3.5 py-3.5">
            <Skeleton className="mt-1 h-2.5 w-5 shrink-0" />
            <div className="flex flex-1 flex-col gap-2">
              <Skeleton className="h-3.5 w-56 max-w-full" />
              <Skeleton className="h-3 w-80 max-w-full" />
            </div>
            <Skeleton className="mt-0.5 hidden h-4 w-20 shrink-0 narrow:block" />
            <Skeleton className="mt-1 hidden h-2.5 w-28 shrink-0 narrow:block" />
            <Skeleton className="h-7 w-20 shrink-0 rounded-full" />
          </div>
        ))}
      </div>

      <span role="status" className="sr-only">
        {dict.participant.loading}
      </span>
    </Band>
  );
}
