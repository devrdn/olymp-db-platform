import Link from "next/link";
import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { SignOutButton } from "@/components/layout/sign-out-button";
import { Avatar } from "@/components/ui/avatar";
import { Tag } from "@/components/ui/tag";
import { initials } from "@/lib/format/initials";
import { fetchIdentity } from "@/lib/auth/session";
import { activeDictionary } from "@/lib/i18n/server";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.profile.heading };
}

/**
 * The account, and the two things one can do to it.
 *
 * It exists because the bar needed a door that leads somewhere: signing out
 * and changing a password are account actions, and putting them in the chrome
 * would spend the bar on controls most people touch twice a year — against the
 * design system's first rule, that the interface around the data is rules and
 * typography.
 *
 * Roles rather than permissions. `/auth/me` reports both; permissions answer
 * "may I offer this button" and are the interface's business, while a role is
 * what a person would say they are. Twelve permission codes in a list tell a
 * student nothing they asked.
 */
export default async function ProfilePage() {
  const [dict, identity] = await Promise.all([activeDictionary(), fetchIdentity()]);
  if (!identity) redirect("/login?next=%2Fprofile");

  const t = dict.profile;
  const name = identity.fullName.trim() || identity.login;

  return (
    <Band className="gap-10 pt-9 pb-7">
      <div className="flex min-w-0 flex-col gap-6">
        <div className="flex min-w-0 items-center gap-4">
          <Avatar letters={initials(identity.fullName, identity.login)} className="size-12 text-h3" />
          <div className="flex min-w-0 flex-col gap-1">
            <h1 className="max-w-head truncate text-h2 text-ink">{name}</h1>
            <p className="max-w-lede text-body text-ink-3">{t.lede}</p>
          </div>
        </div>

        {/* A definition list, because that is what this is: a field and what
            it holds. The same shape the audit trail uses for a change set. */}
        <dl className="flex flex-col gap-3 border-t border-line pt-6">
          <Row label={t.login}>
            <span className="font-mono text-data text-ink-2">{identity.login}</span>
          </Row>
          <Row label={t.email}>
            <span className="font-mono text-data text-ink-2">
              {identity.email || <span className="text-ink-3">{t.noEmail}</span>}
            </span>
          </Row>
          <Row label={t.roles}>
            {identity.roles.length > 0 ? (
              <span className="flex flex-wrap gap-1.5">
                {identity.roles.map((role) => (
                  <Tag key={role}>{role}</Tag>
                ))}
              </span>
            ) : (
              <span className="font-mono text-data text-ink-3">{t.noRoles}</span>
            )}
          </Row>
        </dl>

        <div className="flex flex-wrap items-center gap-4 border-t border-line pt-6">
          <Link
            href="/password"
            className="text-control text-ink-2 underline underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink"
          >
            {t.changePassword}
          </Link>
          <span aria-hidden className="h-4 w-px bg-line-2" />
          <SignOutButton label={t.signOut} withLabel />
          {/* Said rather than discovered: changing a password ends every
              session, signing out ends only this one, and the difference is
              what somebody on a shared machine needs to know. */}
          <span className="text-body text-ink-3">{t.signOutNote}</span>
        </div>
      </div>
    </Band>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
      <dt className="w-24 shrink-0 font-mono text-label text-ink-3 uppercase">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}
