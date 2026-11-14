import type { ProviderId } from "@/lib/auth/external";

const SIZE = 18;

const GITHUB_MARK = 'url("/brand/github-mark.svg")';

/** The mark beside a provider's name; decorative, since the button's text carries the name. */
export function ProviderLogo({ provider }: { provider: ProviderId }) {
  if (provider === "microsoft") {
    return (
      <svg width={SIZE} height={SIZE} viewBox="0 0 21 21" aria-hidden="true" focusable="false">
        <rect x="1" y="1" width="9" height="9" fill="#F25022" />
        <rect x="11" y="1" width="9" height="9" fill="#7FBA00" />
        <rect x="1" y="11" width="9" height="9" fill="#00A4EF" />
        <rect x="11" y="11" width="9" height="9" fill="#FFB900" />
      </svg>
    );
  }
  if (provider === "google") {
    return (
      // Not next/image: a fixed static mark that needs no resizing pipeline.
      // eslint-disable-next-line @next/next/no-img-element
      <img
        src="/brand/google-g.png"
        alt=""
        aria-hidden="true"
        className="shrink-0 object-contain"
        style={{ width: SIZE, height: SIZE }}
      />
    );
  }
  // The mark is a mask over the text colour, so it follows the theme.
  return (
    <span
      aria-hidden="true"
      className="inline-block shrink-0 bg-current"
      style={{
        width: SIZE,
        height: SIZE,
        maskImage: GITHUB_MARK,
        WebkitMaskImage: GITHUB_MARK,
        maskSize: "contain",
        WebkitMaskSize: "contain",
        maskRepeat: "no-repeat",
        WebkitMaskRepeat: "no-repeat",
        maskPosition: "center",
        WebkitMaskPosition: "center",
      }}
    />
  );
}
