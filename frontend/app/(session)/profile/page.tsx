import Link from "next/link";
import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { SignOutButton } from "@/components/layout/sign-out-button";
import { Avatar } from "@/components/ui/avatar";
import { Tag } from "@/components/ui/tag";
import { profileContestsSchema, profileSummarySchema } from "@/lib/api/profile";
import { serverRequest } from "@/lib/api/server";
import { authRecoveryRedirect } from "@/lib/auth/guard";
import { fetchIdentity } from "@/lib/auth/session";
import { initials } from "@/lib/format/initials";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { ContestList } from "./contest-list";
import { ProfileSummary } from "./summary";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.profile.heading };
}

/**
 * The account: who is signed in, what they have done, and which contests were
 * theirs.
 *
 * It exists because the bar needed a door that leads somewhere: signing out
 * and changing a password are account actions, and putting them in the chrome
 * would spend the bar on controls most people touch twice a year — against the
 * design system's first rule, that the interface around the data is rules and
 * typography. What it grew into is the participant's own half of the product
 * (design §2.1): their record, their contests and, from a finished one, their
 * report.
 *
 * Roles rather than permissions. `/auth/me` reports both; permissions answer
 * "may I offer this button" and are the interface's business, while a role is
 * what a person would say they are. Twelve permission codes in a list tell a
 * student nothing they asked.
 *
 * There is no "member since": `/auth/me` does not report when the account was
 * created, and the profile reads are about contests rather than about the
 * account. A date invented from anything else would be a date.
 *
 * Server-rendered, so the session stays on the server and the first paint
 * carries the rows. The two reads are independent and are treated as such: a
 * failed one costs its own section a line of explanation, never the page. A
 * student whose summary times out still needs the sign-out button.
 */
export default async function ProfilePage() {
  const [dict, locale, identity] = await Promise.all([
    activeDictionary(),
    activeLocale(),
    fetchIdentity(),
  ]);
  if (!identity) redirect("/login?next=%2Fprofile");

  const [summary, contests] = await Promise.all([
    read("/me/summary", (payload) => profileSummarySchema.parse(payload)),
    read("/me/contests", (payload) => profileContestsSchema.parse(payload)),
  ]);

  const t = dict.profile;
  const name = identity.fullName.trim() || identity.login;

  return (
    <Band className="gap-10 pt-9 pb-7">
      <div className="flex min-w-0 flex-col gap-8">
        <div className="flex min-w-0 flex-col gap-6">
          <div className="flex min-w-0 items-center gap-5">
            <Avatar letters={initials(identity.fullName, identity.login)} className="size-14 text-h3" />
            <div className="flex min-w-0 flex-col gap-1">
              <h1 className="max-w-head truncate text-h2 text-ink">{name}</h1>
              <p className="max-w-lede text-body text-ink-3">{t.lede}</p>
            </div>
          </div>

          {/* Still a definition list — a field and what it holds is what this
              is — but laid along one line rather than stacked into a form.
              Three labelled values are a header; three rows of them were the
              screen the redesign was asked to replace. */}
          <dl className="flex flex-wrap items-baseline gap-x-7 gap-y-2.5">
            <Pair label={t.login}>
              <span className="font-mono text-data text-ink-2">{identity.login}</span>
            </Pair>
            <Pair label={t.email}>
              <span className="font-mono text-data text-ink-2">
                {identity.email || <span className="text-ink-3">{t.noEmail}</span>}
              </span>
            </Pair>
            <Pair label={t.roles}>
              {identity.roles.length > 0 ? (
                <span className="flex flex-wrap gap-1.5">
                  {identity.roles.map((role) => (
                    <Tag key={role}>{role}</Tag>
                  ))}
                </span>
              ) : (
                <span className="font-mono text-data text-ink-3">{t.noRoles}</span>
              )}
            </Pair>
          </dl>

          <div className="flex flex-wrap items-center gap-4">
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

        <ProfileSummary summary={summary} dict={dict} />
        <ContestList contests={contests} dict={dict} locale={locale} />
      </div>
    </Band>
  );
}

/**
 * One of the profile's reads, with its failure kept to itself.
 *
 * A dead session or an account still on its one-time password is not a failure
 * of this page and gets the recovery the guard decides; everything else — the
 * API being down, a body that does not parse — becomes `null`, which the
 * section renders as one line. The reason goes to the server log, because
 * otherwise it exists nowhere: the page will have rendered successfully.
 */
async function read<T>(path: string, parse: (payload: unknown) => T): Promise<T | null> {
  try {
    return parse(await serverRequest(path));
  } catch (error: unknown) {
    const target = authRecoveryRedirect(error, "/profile");
    if (target) redirect(target);
    console.error("reading %s for the profile failed", path, error);
    return null;
  }
}

function Pair({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline gap-2.5">
      <dt className="shrink-0 font-mono text-label text-ink-3 uppercase">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}
