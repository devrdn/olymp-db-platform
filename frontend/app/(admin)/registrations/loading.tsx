import { Band } from "@/components/layout/band";
import { Skeleton } from "@/components/ui/skeleton";
import { activeDictionary } from "@/lib/i18n/server";

export default async function Loading() {
  const dict = await activeDictionary();

  return (
    <Band fill className="flex flex-col gap-8 py-12">
      <Skeleton className="h-10 w-56 max-w-full rounded-none" />
      <Skeleton className="h-(--control-h) w-96 max-w-full rounded-none" />
      <div aria-hidden className="border-t border-line-2">
        {[0, 1, 2, 3, 4].map((row) => (
          <div key={row} className="flex items-center gap-4 border-b border-line py-3.5">
            <Skeleton className="h-3.5 w-56 max-w-full" />
            <Skeleton className="ml-auto h-(--control-h) w-24 shrink-0 rounded-full" />
          </div>
        ))}
      </div>
      <span role="status" className="sr-only">
        {dict.registration.desk.loading}
      </span>
    </Band>
  );
}
