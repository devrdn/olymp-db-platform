import Link from "next/link";
import { redirect } from "next/navigation";

import { Band } from "@/components/layout/band";
import { SignOutButton } from "@/components/layout/sign-out-button";
import { FeedbackLink } from "@/components/product/feedback-link";
import { Avatar } from "@/components/ui/avatar";
import { Tag } from "@/components/ui/tag";
import { branding } from "@/lib/api/branding";
import { profileContestsSchema, profileSummarySchema } from "@/lib/api/profile";
import { readPage } from "@/lib/api/page-read";
import { feedbackHref } from "@/lib/api/settings";
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
 * The account: who is signed in, their record and their contests (SPEC.md §5.2),
 * plus sign-out, password change and the installation's feedback link, kept
 * out of the bar.
 *
 * Roles, not permissions: a role is what a person would say they are. No
 * "member since": `/auth/me` does not report it. The two reads are independent,
 * so a failure costs one section a line, never the page.
 */
export default async function ProfilePage() {
  // `branding` shares the layout's request (React's `cache()`).
  const [dict, locale, identity, brand] = await Promise.all([
    activeDictionary(),
    activeLocale(),
    fetchIdentity(),
    branding(),
  ]);
  if (!identity) redirect("/login?next=%2Fprofile");

  const [summary, contests] = await Promise.all([
    readPage("/me/summary", (payload) => profileSummarySchema.parse(payload), PROFILE_READ),
    readPage("/me/contests", (payload) => profileContestsSchema.parse(payload), PROFILE_READ),
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

          {/* A definition list laid out on one line. */}
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
            {feedbackHref(brand.feedback) !== null ? (
              <>
                <span aria-hidden className="h-4 w-px bg-line-2" />
                <FeedbackLink href={brand.feedback} labels={dict.participant.feedback} className="text-control" />
              </>
            ) : null}
            <span aria-hidden className="h-4 w-px bg-line-2" />
            <SignOutButton label={t.signOut} withLabel />
            {/* Changing the password ends every session, signing out only this
               one; that matters on a shared machine. */}
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
 * A failed profile read costs its section one line, never the page. The
 * reader's own read budget is no failure worth logging.
 */
const PROFILE_READ = { here: "/profile", absent: ["profile_too_often"], bestEffort: true } as const;

function Pair({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline gap-2.5">
      <dt className="shrink-0 font-mono text-label text-ink-3 uppercase">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}
