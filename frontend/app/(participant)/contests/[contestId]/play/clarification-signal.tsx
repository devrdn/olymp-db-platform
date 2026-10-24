"use client";

import { createContext, useContext, useMemo, useState } from "react";

/**
 * Carries what the events channel says about the questions to the organisers
 * from the header, which holds the channel, to the side panel's tab, which
 * reads the feed. The two are siblings across a Suspense boundary.
 *
 * `hints` counts `clarification` events, `connections` counts admitted
 * connections (a hint sent while the channel was away is lost, so a fresh
 * connection is one too), and `finished` is the channel's word that the
 * contest is over for this participant. Outside a provider nothing ever
 * changes.
 */
export type ClarificationSignal = {
  hints: number;
  connections: number;
  finished: boolean;
};

export type ClarificationPublisher = {
  hint: () => void;
  connected: () => void;
  finish: () => void;
};

const quiet: ClarificationSignal = { hints: 0, connections: 0, finished: false };
const noop: ClarificationPublisher = { hint: () => {}, connected: () => {}, finish: () => {} };

const SignalContext = createContext<ClarificationSignal>(quiet);
const PublisherContext = createContext<ClarificationPublisher>(noop);

export function ClarificationSignalProvider({ children }: { children: React.ReactNode }) {
  const [signal, setSignal] = useState(quiet);
  // Stable, so the header's channel never reconnects for a new callback.
  const publisher = useMemo<ClarificationPublisher>(
    () => ({
      hint: () => setSignal((s) => ({ ...s, hints: s.hints + 1 })),
      connected: () => setSignal((s) => ({ ...s, connections: s.connections + 1 })),
      finish: () => setSignal((s) => (s.finished ? s : { ...s, finished: true })),
    }),
    [],
  );
  return (
    <PublisherContext.Provider value={publisher}>
      <SignalContext.Provider value={signal}>{children}</SignalContext.Provider>
    </PublisherContext.Provider>
  );
}

export function useClarificationSignal(): ClarificationSignal {
  return useContext(SignalContext);
}

export function useClarificationPublisher(): ClarificationPublisher {
  return useContext(PublisherContext);
}
