import Link from "next/link";

import { Avatar } from "@/components/ui/avatar";
import { initials } from "@/lib/format/initials";

export type Account = { fullName: string; login: string };

/**
 * Link to the profile, where account actions (sign-out included) live. The name
 * is the accessible name and the initials are decoration; in the bar on a
 * narrow screen only the circle shows, and in the bar's menu the whole name.
 */
export function AccountLink({ account, layout = "bar" }: { account: Account; layout?: "bar" | "menu" }) {
  const name = account.fullName.trim() || account.login;

  if (layout === "menu") {
    return (
      <Link
        href="/profile"
        className="flex min-h-12 min-w-0 items-center gap-2.5 text-control text-ink-2 transition-colors duration-(--t-input) ease-standard hover:text-ink"
      >
        <Avatar letters={initials(account.fullName, account.login)} />
        <span className="min-w-0 truncate">{name}</span>
      </Link>
    );
  }

  return (
    <Link
      href="/profile"
      aria-label={name}
      title={name}
      className="flex shrink-0 items-center gap-2 rounded-full text-control text-ink-3 transition-colors duration-(--t-input) ease-standard hover:text-ink"
    >
      <Avatar letters={initials(account.fullName, account.login)} />
      <span className="max-w-32 truncate max-narrow:hidden">{name}</span>
    </Link>
  );
}
