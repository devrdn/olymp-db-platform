"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";

/**
 * What the page below the header rendered, told to the header above it: that
 * the workspace loaded, or which refusal it showed instead.
 *
 * The two are siblings on the page, split by a `<Suspense>` boundary so the
 * header streams first (`page.tsx`'s own doc). The header cannot see what
 * resolved under that boundary, and two of its decisions depend on it.
 *
 * Under individual timing the reads the workspace is built from are what
 * start the participant's clock on the server, and the header's events
 * channel can have opened, and sent its first sync with no deadline, before
 * they finished. `ContentLoadedSignal` is how the header learns they
 * succeeded, so it can ask for a fresh sync instead of showing a countdown
 * that "has not started" to somebody whose clock is running.
 *
 * A refusal shown in the workspace's place can go stale under a header that
 * keeps running: a contest that was not open now when the page rendered may
 * open a moment later, before the header's channel ever connects. That
 * channel is then simply admitted, and only the page knows it rendered the
 * refusal. `RenderedRefusalSignal` carries the code up, and the header
 * decides what it means (play-header.tsx).
 *
 * Outside a provider nothing is ever marked loaded or refused, which is the
 * safe reading for a header rendered on its own (the waiting room).
 */
type ContentLoaded = {
  loaded: boolean;
  markLoaded: () => void;
  /** The refusal code the page is showing in the workspace's place, or null. */
  refusal: string | null;
  setRefusal: (code: string | null) => void;
};

const ContentLoadedContext = createContext<ContentLoaded>({
  loaded: false,
  markLoaded: () => {},
  refusal: null,
  setRefusal: () => {},
});

export function ContentLoadedProvider({ children }: { children: React.ReactNode }) {
  const [loaded, setLoaded] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);
  const markLoaded = useCallback(() => setLoaded(true), []);
  const value = useMemo(() => ({ loaded, markLoaded, refusal, setRefusal }), [loaded, markLoaded, refusal]);
  return <ContentLoadedContext.Provider value={value}>{children}</ContentLoadedContext.Provider>;
}

/** Reads whether the workspace has loaded. */
export function useContentLoaded(): boolean {
  return useContext(ContentLoadedContext).loaded;
}

/** Reads the refusal the page is showing in the workspace's place, or null when it shows none. */
export function useRenderedRefusal(): string | null {
  return useContext(ContentLoadedContext).refusal;
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

/**
 * Rendered next to a refusal shown in the workspace's place. It holds the
 * code for as long as it is mounted: once a refresh replaces the refusal with
 * the workspace, the header no longer reads it as the page's state.
 */
export function RenderedRefusalSignal({ code }: { code: string }) {
  const { setRefusal } = useContext(ContentLoadedContext);
  useEffect(() => {
    setRefusal(code);
    return () => setRefusal(null);
  }, [code, setRefusal]);
  return null;
}
