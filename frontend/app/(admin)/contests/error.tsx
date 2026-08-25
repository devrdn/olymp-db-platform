"use client";

import { useEffect } from "react";

/**
 * The recoverable-error state (spec §7): a cause and a way forward.
 *
 * Next strips a Server Component error down to a digest before it reaches the
 * browser, so the API's machine code is not available here and this screen does
 * not pretend to know it. What it can honestly offer is a retry, which is the
 * whole point of the state: the failure class that reaches this boundary is
 * network or 5xx, and both are worth trying again.
 */
export default function ContestsError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    console.error(error);
  }, [error]);

  return (
    <main className="mx-auto w-full max-w-6xl px-4 py-10 sm:px-6 lg:px-8">
      <div className="flex flex-col items-start gap-3 border-t border-line py-12">
        <p className="font-medium">Не удалось загрузить список олимпиад</p>
        <p className="max-w-[52ch] text-sm text-ink-2">
          Сервер не ответил. Проверьте соединение и попробуйте ещё раз.
        </p>
        <button
          type="button"
          onClick={reset}
          className="mt-1 rounded-full bg-cta px-4 py-1.5 text-sm font-semibold text-cta-fg transition-opacity duration-150 hover:opacity-90 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
        >
          Повторить
        </button>
        {error.digest ? (
          <p className="pt-2 font-mono text-[0.6875rem] text-ink-3">
            Код обращения: {error.digest}
          </p>
        ) : null}
      </div>
    </main>
  );
}
