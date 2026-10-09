import Link from "next/link";

import { Avatar } from "@/components/ui/avatar";
import { initials } from "@/lib/format/initials";

export type Account = { fullName: string; login: string };

/**
 * Link to the profile, where account actions (sign-out included) live. The name
 * is the accessible name and the initials are decoration; on a narrow screen
 * only the circle shows.
 */
export function AccountLink({ account }: { account: Account }) {
  const name = account.fullName.trim() || account.login;

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
