/**
 * The loading state is a skeleton shaped like the register, not a spinner:
 * a spinner says "wait", a skeleton says "here is what is coming" (spec §7).
 * Its rows match the real row height, so nothing jumps when data arrives.
 */
export default function Loading() {
  return (
    <main className="mx-auto w-full max-w-6xl px-4 py-10 sm:px-6 lg:px-8">
      <div className="flex items-baseline justify-between gap-4 px-1 pb-3">
        <div className="h-6 w-32 rounded bg-line-2" />
        <div className="h-3 w-20 rounded bg-line" />
      </div>
      <div className="border-t border-line" aria-hidden>
        {[0, 1, 2, 3].map((row) => (
          <div key={row} className="flex items-center gap-4 border-b border-line px-3 py-4">
            <div className="h-3 w-5 rounded bg-line" />
            <div className="flex-1 space-y-2">
              <div className="h-3.5 w-52 rounded bg-line-2" />
              <div className="h-3 w-80 max-w-full rounded bg-line" />
            </div>
            <div className="hidden h-3 w-20 rounded bg-line sm:block" />
            <div className="hidden h-3 w-24 rounded bg-line md:block" />
          </div>
        ))}
      </div>
      <span className="sr-only" role="status">
        Загружается список олимпиад
      </span>
    </main>
  );
}
