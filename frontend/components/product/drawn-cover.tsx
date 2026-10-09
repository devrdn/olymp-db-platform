import { cn } from "@/lib/utils";

/**
 * The cover a contest wears when nobody uploaded one. Same aspect, scrim and
 * title placement as a photograph, so a mixed row reads as one row
 * (ARCHITECTURE.md §9.7).
 *
 * Deterministic in the contest's identifier: nothing random, stored or fetched,
 * so a contest looks the same on every visit. Uses only theme tokens, so it
 * follows the dark theme.
 */
export function DrawnCover({
  seed,
  label,
  className,
}: {
  /** The contest's identifier; the same one always draws the same cover. */
  seed: string;
  /**
   * Accessible name, set only where the drawing stands alone (the settings
   * panel). On a card the title is printed over it, so it is left off.
   */
  label?: string;
  className?: string;
}) {
  const plan = layout(seed);

  return (
    <div
      {...(label ? { role: "img", "aria-label": label } : { "aria-hidden": true })}
      className={cn("relative size-full overflow-hidden bg-accent-wash", className)}
    >
      {/* The ground, raked at the identifier's angle; only where the light falls
         varies, never the colours. */}
      <div
        className="absolute inset-0"
        style={{
          backgroundImage: `linear-gradient(${plan.angle}deg, var(--accent-wash) 0%, var(--accent-wash) 20%, var(--panel) 74%, var(--bg) 100%)`,
        }}
      />

      <div
        className="absolute inset-0"
        style={{
          backgroundImage: `radial-gradient(62% 70% at ${plan.lightX}% ${plan.lightY}%, var(--glow) 0%, transparent 70%)`,
        }}
      />

      <div className="hatched absolute inset-0" />

      {/* Keeps to the upper two thirds; the bottom third belongs to the scrim and title. */}
      <svg
        viewBox="0 0 160 90"
        preserveAspectRatio="xMidYMid slice"
        className="absolute inset-0 size-full"
      >
        <g transform={`rotate(${plan.tilt} ${plan.ringX} ${plan.ringY})`}>
          {/* Long enough to leave the frame at both ends for any tilt and ring position. */}
          <line
            x1={plan.ringX - 200}
            y1={plan.ringY}
            x2={plan.ringX + 200}
            y2={plan.ringY}
            stroke="var(--line-2)"
            strokeWidth="0.5"
          />
          <circle
            cx={plan.ringX}
            cy={plan.ringY}
            r={plan.ringR}
            fill="none"
            stroke="var(--accent)"
            strokeOpacity="0.26"
            strokeWidth="0.75"
          />
          <circle cx={plan.ringX} cy={plan.ringY} r={plan.ringR * 0.55} fill="var(--glow)" />
        </g>
      </svg>
    </div>
  );
}

/**
 * FNV-1a over the identifier. UUIDv7s created together differ only in their
 * last characters, and FNV-1a spreads a change in any byte across the result.
 * Not a security hash.
 */
function fingerprint(seed: string): number {
  let hash = 0x811c9dc5;
  for (let index = 0; index < seed.length; index += 1) {
    hash ^= seed.charCodeAt(index);
    // The FNV prime by shift and add: a plain multiplication overflows into a
    // float and loses the low bits.
    hash = (hash + ((hash << 1) + (hash << 4) + (hash << 7) + (hash << 8) + (hash << 24))) >>> 0;
  }
  return hash >>> 0;
}

type Plan = {
  angle: number;
  lightX: number;
  lightY: number;
  ringX: number;
  ringY: number;
  ringR: number;
  tilt: number;
};

/**
 * Each field reads its own slice of the fingerprint, so contests differ in more
 * than one way. The ranges keep every draw a cover: the ring in the upper two
 * thirds, the light off the bottom edge, the angle short of vertical.
 * Coordinates are in the SVG's `160 × 90`.
 */
function layout(seed: string): Plan {
  const hash = fingerprint(seed);
  const pick = (shift: number, count: number) => (hash >>> shift) % count;

  return {
    angle: 15 + pick(0, 11) * 15,
    lightX: 16 + pick(4, 10) * 8,
    lightY: 12 + pick(8, 6) * 9,
    ringX: 18 + pick(12, 13) * 8,
    ringY: 16 + pick(16, 7) * 5,
    ringR: 20 + pick(20, 5) * 6,
    tilt: pick(24, 5) * 9 - 18,
  };
}
