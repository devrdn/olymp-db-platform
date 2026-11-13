import { LogIn } from "lucide-react";

import type { ProviderId } from "@/lib/auth/external";

const SIZE = 18;

/**
 * The mark beside a provider's name; decorative, since the button's text
 * carries the name. Google and GitHub show a neutral icon until their official
 * marks replace it, here and nowhere else.
 */
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
  return <LogIn size={SIZE} aria-hidden="true" focusable="false" />;
}
