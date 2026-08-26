"use client";

/**
 * The failure that took the root layout with it.
 *
 * This replaces the document, so it has no access to the fonts, the tokens or
 * the dictionary — everything they live in is what has just failed. It is
 * therefore deliberately plain and deliberately English, and the one thing it
 * carries is the digest, which is what ties the screen to a line in the log.
 *
 * Anything richer here would be a second design system maintained for the case
 * where the first one is broken.
 */
export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return (
    <html lang="en">
      <body
        style={{
          margin: 0,
          minHeight: "100dvh",
          display: "grid",
          placeItems: "center",
          fontFamily: "system-ui, sans-serif",
          background: "#ffffff",
          color: "#0d1117",
        }}
      >
        <main style={{ maxWidth: "40ch", padding: "2rem", textAlign: "left" }}>
          <h1 style={{ fontSize: "1.25rem", fontWeight: 500, margin: 0 }}>
            DB Contest could not start
          </h1>
          <p style={{ color: "#4c535e", lineHeight: 1.6 }}>
            The page failed before the interface loaded. Reloading usually helps.
          </p>
          <button
            type="button"
            onClick={reset}
            style={{
              cursor: "pointer",
              border: 0,
              borderRadius: 999,
              padding: "0.5rem 1rem",
              background: "#0d1117",
              color: "#ffffff",
              font: "inherit",
            }}
          >
            Reload
          </button>
          {error.digest ? (
            <p style={{ fontFamily: "ui-monospace, monospace", fontSize: "0.75rem", color: "#66696d" }}>
              Reference: {error.digest}
            </p>
          ) : null}
        </main>
      </body>
    </html>
  );
}
