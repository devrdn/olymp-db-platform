"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";

/**
 * Tells the header what the page below rendered: the workspace, or a refusal
 * in its place. The two are siblings across a Suspense boundary, so the header
 * cannot see it otherwise.
 *
 * Under individual timing the workspace's reads start the clock, possibly
 * after the header's channel sent a sync with no deadline;
 * `ContentLoadedSignal` lets the header ask for a fresh sync. A "not open now"
 * refusal can go stale if the contest opens before the header's channel
 * connects, and only the page knows it rendered one; `RenderedRefusalSignal`
 * carries the code up (play-header.tsx decides what it means).
 *
 * Outside a provider nothing is loaded or refused, the safe reading for the
 * waiting room's header.
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

/** Whether the workspace has loaded. */
export function useContentLoaded(): boolean {
  return useContext(ContentLoadedContext).loaded;
}

/** The refusal shown in the workspace's place, or null. */
export function useRenderedRefusal(): string | null {
  return useContext(ContentLoadedContext).refusal;
}

/** Mounted beside the workspace only when its content reads succeeded. */
export function ContentLoadedSignal() {
  const { markLoaded } = useContext(ContentLoadedContext);
  useEffect(() => {
    markLoaded();
  }, [markLoaded]);
  return null;
}

/**
 * Mounted beside a refusal shown in the workspace's place; holds the code
 * while mounted, so a refresh that brings the workspace clears it.
 */
export function RenderedRefusalSignal({ code }: { code: string }) {
  const { setRefusal } = useContext(ContentLoadedContext);
  useEffect(() => {
    setRefusal(code);
    return () => setRefusal(null);
  }, [code, setRefusal]);
  return null;
}
