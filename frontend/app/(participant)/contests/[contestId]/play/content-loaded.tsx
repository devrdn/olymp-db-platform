"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";

/**
 * Whether the workspace below the header has loaded, told to the header above
 * it.
 *
 * The two are siblings on the page, split by a `<Suspense>` boundary so the
 * header streams first (`page.tsx`'s own doc). Under individual timing that
 * split has a consequence: the reads the workspace is built from are what
 * start the participant's clock on the server, and the header's events
 * channel can have opened, and sent its first sync with no deadline, before
 * they finished. The header cannot see the reads; this is how it learns they
 * succeeded, so it can ask for a fresh sync instead of showing a countdown
 * that "has not started" to somebody whose clock is running.
 *
 * Outside a provider nothing is ever marked loaded, which is the safe reading
 * for a header rendered on its own (the waiting room).
 */
type ContentLoaded = { loaded: boolean; markLoaded: () => void };

const ContentLoadedContext = createContext<ContentLoaded>({ loaded: false, markLoaded: () => {} });

export function ContentLoadedProvider({ children }: { children: React.ReactNode }) {
  const [loaded, setLoaded] = useState(false);
  const markLoaded = useCallback(() => setLoaded(true), []);
  const value = useMemo(() => ({ loaded, markLoaded }), [loaded, markLoaded]);
  return <ContentLoadedContext.Provider value={value}>{children}</ContentLoadedContext.Provider>;
}

/** Reads whether the workspace has loaded. */
export function useContentLoaded(): boolean {
  return useContext(ContentLoadedContext).loaded;
}

/**
 * Rendered next to the workspace, only on the path where its content reads
 * succeeded: mounting it is the signal.
 */
export function ContentLoadedSignal() {
  const { markLoaded } = useContext(ContentLoadedContext);
  useEffect(() => {
    markLoaded();
  }, [markLoaded]);
  return null;
}
