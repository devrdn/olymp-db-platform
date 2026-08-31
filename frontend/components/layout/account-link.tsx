import Link from "next/link";

import { Avatar } from "@/components/ui/avatar";
import { initials } from "@/lib/format/initials";

/** Who is signed in, as much of it as the bar needs. */
export type Account = { fullName: string; login: string };

/**
 * The way into one's own account, in the bar.
 *
 * A link and not a menu. A dropdown would hide two destinations behind a press
 * and add a client component to every page, where a link costs nothing and the
 * profile screen is where account actions belong anyway — including signing
 * out, which is a thing worth one deliberate step rather than a stray click
 * beside the language switcher.
 *
 * The name is the accessible name and the initials are decoration: an avatar
 * that announced "I I" beside "Ivan Ivanov" would read the same person twice.
 * On a narrow screen the name is dropped and the circle carries it alone,
 * which is why the name is not the only thing identifying the link.
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
