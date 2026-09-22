import { cn } from "@/lib/utils";

/**
 * The cover a contest wears when nobody uploaded one.
 *
 * **Not a placeholder.** A grid in which half the cards carry a photograph
 * and half carry a grey rectangle saying "no image" looks unfinished, and it
 * looks unfinished on the day the installation opens, when no contest has a
 * photograph at all. So this is a cover of the same family as the
 * photographs: the same `16/9`, the same scrim over it, the same title in the
 * same place. A mixed row has to read as one row (design spec
 * `docs/superpowers/specs/2026-09-22-contest-covers-design.md` §2.3).
 *
 * **Deterministic in the contest's identifier**, which is what separates a
 * cover from decoration: a drawing that changed between two loads would make
 * one olympiad look like a different olympiad each time somebody came back
 * to it. Nothing here is random, nothing is stored and nothing is fetched —
 * the identifier is the whole input.
 *
 * **No colour of its own.** Every surface is a token already in the system:
 * the ground, the panel, the accent and its wash, the glow behind the front
 * page's title and the hatch that holds the page's own margins. That is not
 * tidiness — it is what makes the drawing follow the theme into the dark one
 * rather than sitting in a dark row as a bright rectangle.
 */
export function DrawnCover({
  seed,
  label,
  className,
}: {
  /** The contest's identifier. The same one always draws the same cover. */
  seed: string;
  /**
   * An accessible name, for the one place the drawing stands on its own: the
   * organiser's settings panel, where it answers "what will this contest
   * wear?". On a card it is left off, because the title is printed over it
   * and a picture that repeats what the page already says is noise to
   * somebody reading with their ears.
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
      {/* The ground, raked at the angle the identifier chose. Three stops of
          the same three tokens: what changes between two contests is where
          the light falls, never which colours are in play. */}
      <div
        className="absolute inset-0"
        style={{
          backgroundImage: `linear-gradient(${plan.angle}deg, var(--accent-wash) 0%, var(--accent-wash) 20%, var(--panel) 74%, var(--bg) 100%)`,
        }}
      />

      {/* One soft source, as on the front page's own title band: a surface lit
          from somewhere rather than a shape drawn on it. */}
      <div
        className="absolute inset-0"
        style={{
          backgroundImage: `radial-gradient(62% 70% at ${plan.lightX}% ${plan.lightY}%, var(--glow) 0%, transparent 70%)`,
        }}
      />

      {/* The direction's own surface, the one that holds the page's margins. */}
      <div className="hatched absolute inset-0" />

      {/* The geometry, and deliberately little of it: a ring, its centre and
          the rule it sits on. It keeps to the upper two thirds, because the
          bottom third belongs to the scrim and the contest's title. */}
      <svg
        viewBox="0 0 160 90"
        preserveAspectRatio="xMidYMid slice"
        className="absolute inset-0 size-full"
      >
        <g transform={`rotate(${plan.tilt} ${plan.ringX} ${plan.ringY})`}>
          {/* Long enough to leave the frame at either end whatever the tilt
              and wherever the ring sits: a rule that stopped in mid-air would
              read as a mistake rather than as a margin. */}
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
 * The identifier folded into one number.
 *
 * FNV-1a, because of the shape of the input rather than for its speed.
 * Identifiers arrive in runs — a UUIDv7 is written in creation order, so the
 * contests of one afternoon differ only in their last few characters — and a
 * fold that let those runs through would give a whole installation one
 * drawing. FNV-1a changes every bit of the result on a change to any byte,
 * which is exactly the property wanted here and the only one: this is not a
 * hash anything is protected by.
 */
function fingerprint(seed: string): number {
  let hash = 0x811c9dc5;
  for (let index = 0; index < seed.length; index += 1) {
    hash ^= seed.charCodeAt(index);
    // The FNV prime, by shift and add: a plain multiplication overflows into
    // a float and loses the low bits that carry the run apart.
    hash = (hash + ((hash << 1) + (hash << 4) + (hash << 7) + (hash << 8) + (hash << 24))) >>> 0;
  }
  return hash >>> 0;
}

/** Where this contest's drawing puts its light, its ring and its rule. */
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
 * The drawing, chosen from the fingerprint one field at a time.
 *
 * Each field reads its own slice of the number, so two contests differ in
 * more than one way rather than sliding along a single axis. The ranges are
 * what keeps every draw a cover rather than a possibility: the ring stays in
 * the upper two thirds, the light stays off the bottom edge, and the angle
 * never reaches the vertical, where the gradient would read as a horizon
 * instead of as a rake of light.
 *
 * Coordinates are in the SVG's own `160 × 90`, which is the aspect the whole
 * cover is.
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
