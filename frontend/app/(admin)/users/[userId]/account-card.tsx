"use client";

import { Fragment, useId } from "react";

import {
  ActionButton,
  ActionFooter,
  ActionForm,
  ActionOutcome,
  ActionReload,
} from "@/components/product/action-form";
import { ProviderLogo } from "@/components/product/provider-logo";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Tooltip } from "@/components/ui/tooltip";
import type { Account, IdentityList, Role } from "@/lib/api/accounts";
import { isProviderId, PROVIDER_NAMES } from "@/lib/auth/external";
import { formatMoment } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill, isolate } from "@/lib/i18n/fill";

import {
  allowIdentityAction,
  blockAction,
  deleteAction,
  replaceRolesAction,
  resetPasswordAction,
  restoreAction,
  unblockAction,
  unlinkIdentityAction,
  unlockSignInAction,
  updateProfileAction,
} from "./actions";
import { EmailSummary, SendByEmailOption, sendingRate, type MailOffer } from "../email-delivery";
import { ReasonField, useRequiredReason } from "../required-reason";
import { offeredActions } from "./offered";

/**
 * One account and its actions. A form per endpoint (profile, roles, each
 * sign-in service, access): these are separate decisions, and a typo fix must
 * not risk a block.
 */

/** A titled block: `help` behind a "?", `hint` a consequence that stays on screen. */
function Panel({
  title,
  hint,
  help,
  helpLabel,
  children,
}: {
  title: string;
  hint?: string;
  help?: string;
  helpLabel?: string;
  children: React.ReactNode;
}) {
  const headingId = useId();

  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-5 border-t border-line pt-5">
      <div className="flex flex-col gap-1.5">
        <div className="flex items-center gap-2">
          <h3 id={headingId} className="text-h3 text-ink">
            {title}
          </h3>
          {help && helpLabel ? <Tooltip label={helpLabel}>{help}</Tooltip> : null}
        </div>
        {hint ? <p className="max-w-body text-small text-ink-2">{hint}</p> : null}
      </div>
      {children}
    </section>
  );
}

/**
 * Clears a sign-in lockout after a confirmation naming the login: it cannot
 * tell the owner from whoever was guessing.
 */
function UnlockSignIn({ account, dict }: { account: Account; dict: Dictionary }) {
  const t = dict.accounts.card;

  return (
    <ActionForm
      action={unlockSignInAction}
      fields={{ userId: account.id }}
      confirm={{
        title: fill(t.unlockSignInConfirmTitle, { login: isolate(account.login) }),
        detail: t.unlockSignInConfirmBody,
        answer: t.unlockSignInConfirm,
        cancel: t.unlockSignInCancel,
      }}
      errors={dict.errors}
      className="flex flex-col gap-2.5"
    >
      <p className="max-w-body text-small text-ink-2">{t.unlockSignInNote}</p>
      <div className="flex flex-wrap items-center gap-4">
        <ActionButton variant="secondary" pendingLabel={t.saving}>
          {t.unlockSignIn}
        </ActionButton>
        <ActionOutcome saved={t.unlocked} />
      </div>
    </ActionForm>
  );
}

/** The brand name, or the id of a provider newer than this build. */
function serviceName(provider: string): string {
  return isProviderId(provider) ? PROVIDER_NAMES[provider] : provider;
}

/** A question naming the service and the email it gave, or the service alone when it gave none. */
function naming(
  titles: { withEmail: string; withoutEmail: string },
  { provider, email }: { provider: string; email: string },
): string {
  const name = serviceName(provider);
  return email
    ? fill(titles.withEmail, { provider: name, email: isolate(email) })
    : fill(titles.withoutEmail, { provider: name });
}

/** One service: its mark and name, its issuer and what is known of it, beside its one form. */
function ServiceRow({
  provider,
  issuer,
  facts,
  children,
}: {
  provider: string;
  issuer: string;
  facts: [label: string, value: string][];
  children: React.ReactNode;
}) {
  return (
    <li className="flex items-start justify-between gap-4 border-t border-line py-4 first:border-t-0 first:pt-0 max-narrow:flex-col">
      <div className="flex min-w-0 flex-col gap-1.5">
        <span className="flex items-center gap-2 text-control text-ink">
          {/* A provider newer than this build has no mark. */}
          {isProviderId(provider) ? <ProviderLogo provider={provider} /> : null}
          {serviceName(provider)}
        </span>
        <span className="font-mono text-data break-all text-ink-3">{issuer}</span>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-small">
          {facts.map(([label, value]) => (
            <Fragment key={label}>
              <dt className="text-ink-3">{label}</dt>
              <dd className="break-all text-ink">{value}</dd>
            </Fragment>
          ))}
        </dl>
      </div>
      {children}
    </li>
  );
}

/**
 * The account's sign-ins through external services, each unlinked after a
 * question naming it, and the ones unlinked from it, each allowed again the
 * same way. Unlinking the only one of an account with no password leaves it
 * no way in, and the question says so.
 */
function SignInServices({
  account,
  identities,
  dict,
  locale,
}: {
  account: Account;
  identities: IdentityList;
  dict: Dictionary;
  locale: Locale;
}) {
  const t = dict.accounts.card;
  const unlinkedHeadingId = useId();

  const lastWayIn = !identities.hasPassword && identities.items.length === 1;
  const unlinkDetail = (provider: string) => {
    const body = fill(t.unlinkIdentityConfirmBody, { provider: serviceName(provider) });
    return lastWayIn ? `${body} ${fill(t.unlinkIdentityLastWay, { action: t.resetPassword })}` : body;
  };

  return (
    <div className="flex flex-col gap-6">
      {identities.items.length === 0 ? (
        <p className="max-w-body text-small text-ink-3">{t.identitiesEmpty}</p>
      ) : (
        <ul className="flex flex-col">
          {identities.items.map((identity) => (
            <ServiceRow
              key={identity.id}
              provider={identity.provider}
              issuer={identity.issuer}
              facts={[
                [t.identityEmail, identity.email || t.identityNoEmail],
                [t.identityLinked, formatMoment(identity.linkedAt, { locale })],
                [t.identityLastUsed, formatMoment(identity.lastUsedAt, { locale })],
              ]}
            >
              <ActionForm
                action={unlinkIdentityAction}
                fields={{ userId: account.id, identityId: identity.id }}
                confirm={{
                  title: naming(
                    { withEmail: t.unlinkIdentityConfirmTitle, withoutEmail: t.unlinkIdentityConfirmTitleNoEmail },
                    identity,
                  ),
                  detail: unlinkDetail(identity.provider),
                  answer: t.unlinkIdentityConfirm,
                  cancel: t.unlinkIdentityCancel,
                  danger: true,
                }}
                errors={dict.errors}
                className="flex flex-col items-start gap-2.5"
              >
                <ActionButton variant="danger" pendingLabel={t.saving}>
                  {t.unlinkIdentity}
                </ActionButton>
                {/* No "saved": the row is gone once the page re-reads. */}
                <ActionOutcome>
                  <ActionReload code="identity_not_found" label={t.identitiesReload} />
                </ActionOutcome>
              </ActionForm>
            </ServiceRow>
          ))}
        </ul>
      )}

      {identities.unlinked.length > 0 ? (
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <h4 id={unlinkedHeadingId} className="text-control text-ink">
              {t.unlinkedHeading}
            </h4>
            <p className="max-w-body text-small text-ink-2">{t.unlinkedHint}</p>
          </div>
          <ul aria-labelledby={unlinkedHeadingId} className="flex flex-col">
            {identities.unlinked.map((unlinked) => (
              <ServiceRow
                key={unlinked.id}
                provider={unlinked.provider}
                issuer={unlinked.issuer}
                facts={[
                  [t.identityEmail, unlinked.email || t.identityNoEmail],
                  [t.identityUnlinked, formatMoment(unlinked.unlinkedAt, { locale })],
                ]}
              >
                <ActionForm
                  action={allowIdentityAction}
                  fields={{ userId: account.id, unlinkedId: unlinked.id }}
                  confirm={{
                    title: naming(
                      { withEmail: t.allowIdentityConfirmTitle, withoutEmail: t.allowIdentityConfirmTitleNoEmail },
                      unlinked,
                    ),
                    detail: t.allowIdentityConfirmBody,
                    answer: t.allowIdentityConfirm,
                    cancel: t.allowIdentityCancel,
                  }}
                  errors={dict.errors}
                  className="flex flex-col items-start gap-2.5"
                >
                  <ActionButton variant="secondary" pendingLabel={t.saving}>
                    {t.allowIdentity}
                  </ActionButton>
                  <ActionOutcome>
                    <ActionReload code="unlinked_identity_not_found" label={t.identitiesReload} />
                  </ActionOutcome>
                </ActionForm>
              </ServiceRow>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}

export function AccountCard({
  account,
  roles,
  identities,
  viewerId,
  dict,
  /** Already formatted for the locale. */
  statusChangedAtLabel,
  locale,
  mail,
}: {
  account: Account;
  roles: Role[];
  /** Null when they could not be read: the panel is left out, not the card. */
  identities: IdentityList | null;
  /** The viewer, so a self-block is not offered. */
  viewerId: string;
  dict: Dictionary;
  statusChangedAtLabel: string | null;
  locale: Locale;
  /** Whether the password reset offers emailing the sign-in details. */
  mail: MailOffer;
}) {
  const t = dict.accounts.card;
  const offered = offeredActions(account, viewerId);

  const blockReason = useRequiredReason(offered.block);
  const deleteReason = useRequiredReason();

  const offersDanger =
    offered.block ||
    offered.unblock ||
    offered.delete ||
    offered.restore ||
    offered.resetPassword ||
    offered.unlockSignIn;

  return (
    <div className="flex flex-col gap-8">
      {/* Only when there is a status reason to show. */}
      {account.statusReason ? (
        <Panel title={t.statusTitle}>
          <div className="flex flex-col gap-2">
            <p className="max-w-body text-body text-ink">{account.statusReason}</p>
            <p className="text-small text-ink-3">
              {/* A backfilled row may lack a timestamp; drop the date half of the sentence. */}
              {statusChangedAtLabel
                ? fill(t.changedBy, { name: account.statusChangedByLogin || t.unknownActor, date: statusChangedAtLabel })
                : fill(t.changedByNoDate, { name: account.statusChangedByLogin || t.unknownActor })}
            </p>
          </div>
        </Panel>
      ) : null}

      {offered.profile ? (
        <Panel title={t.profile}>
          {/* The server's values, so a save replaces the draft. */}
          <ActionForm
            action={updateProfileAction}
            fields={{ userId: account.id }}
            errors={dict.errors}
            revision={account.fullName + account.email}
            className="flex flex-col gap-5"
          >
            <Field id="full_name" label={t.fullName}>
              <Input name="full_name" defaultValue={account.fullName} required />
            </Field>

            <Field id="email" label={t.email}>
              <Input name="email" type="email" defaultValue={account.email ?? ""} />
            </Field>

            <ActionFooter labels={t} />
          </ActionForm>
        </Panel>
      ) : null}

      {offered.roles ? (
        <Panel title={t.roles} hint={t.rolesHint} help={t.rolesHelp} helpLabel={dict.chrome.helpLabel}>
          <ActionForm
            action={replaceRolesAction}
            fields={{ userId: account.id }}
            errors={dict.errors}
            revision={account.roles.join(",")}
            className="flex flex-col gap-5"
          >
            <div className="flex flex-col gap-2.5">
              {roles.map((role) => (
                <label key={role.code} className="flex items-center gap-2.5 text-control text-ink">
                  <input
                    type="checkbox"
                    name="roles"
                    value={role.code}
                    defaultChecked={account.roles.includes(role.code)}
                    className="size-4 accent-cta"
                  />
                  {role.name}
                  <span className="font-mono text-data text-ink-3">{role.code}</span>
                </label>
              ))}
            </div>

            <ActionFooter labels={t} />
          </ActionForm>
        </Panel>
      ) : null}

      {offered.signInServices && identities ? (
        <Panel title={t.identities} hint={t.identitiesHint}>
          <SignInServices account={account} identities={identities} dict={dict} locale={locale} />
        </Panel>
      ) : null}

      {offersDanger ? (
        <Panel title={t.danger}>
          <div className="flex flex-col gap-6">
            {offered.block || offered.unblock ? (
              <ActionForm
                action={account.status === "active" ? blockAction : unblockAction}
                fields={{ userId: account.id }}
                errors={dict.errors}
                onSubmit={blockReason.onSubmit}
                className="flex flex-col gap-2.5"
                noValidate
              >
                <p className="max-w-body text-small text-ink-2">{t.blockNote}</p>

                {offered.block ? (
                  <ReasonField id="block-reason" label={t.reasonLabel} reason={blockReason} />
                ) : null}

                <div className="flex flex-wrap items-center gap-4">
                  <ActionButton variant="danger">{offered.block ? t.block : t.unblock}</ActionButton>
                  <ActionOutcome saved={t.saved} refusal={blockReason.refusal} />
                </div>
              </ActionForm>
            ) : null}

            {offered.delete ? (
              <ActionForm
                action={deleteAction}
                fields={{ userId: account.id }}
                errors={dict.errors}
                onSubmit={deleteReason.onSubmit}
                className="flex flex-col gap-2.5"
                noValidate
              >
                <p className="max-w-body text-small text-ink-2">{t.deleteNote}</p>

                <ReasonField id="delete-reason" label={t.reasonLabel} reason={deleteReason} />

                <div className="flex flex-wrap items-center gap-4">
                  <ActionButton variant="danger" pendingLabel={t.saving}>
                    {t.delete}
                  </ActionButton>
                  <ActionOutcome saved={t.saved} refusal={deleteReason.refusal} />
                </div>
              </ActionForm>
            ) : null}

            {offered.restore ? (
              <ActionForm
                action={restoreAction}
                fields={{ userId: account.id }}
                errors={dict.errors}
                className="flex flex-col gap-2.5"
              >
                <p className="max-w-body text-small text-ink-2">{t.restoreNote}</p>
                <div className="flex flex-wrap items-center gap-4">
                  <ActionButton variant="secondary" pendingLabel={t.saving}>
                    {t.restore}
                  </ActionButton>
                  <ActionOutcome saved={t.saved} />
                </div>
              </ActionForm>
            ) : null}

            {offered.unlockSignIn ? <UnlockSignIn account={account} dict={dict} /> : null}

            {offered.resetPassword ? (
              <ActionForm
                action={resetPasswordAction}
                fields={{ userId: account.id }}
                errors={dict.errors}
                className="flex flex-col gap-2.5"
              >
                {(reset) => (
                  <>
                    <p className="max-w-body text-small text-ink-2">{t.resetNote}</p>
                    {/* Only an active account with an address can be emailed; the
                       server would skip any other. */}
                    {account.status === "active" && account.email ? (
                      <SendByEmailOption mail={mail} dict={dict} />
                    ) : null}
                    <div className="flex flex-wrap items-center gap-4">
                      <ActionButton variant="secondary" pendingLabel={t.saving}>
                        {t.resetPassword}
                      </ActionButton>
                      {/* No "saved": the password below is the answer. */}
                      <ActionOutcome />
                    </div>

                    {/* Shown until the page is left: it arrives once and cannot be retrieved. */}
                    {reset.oneTimePassword ? (
                      <div className="mt-2 flex max-w-body flex-col gap-3">
                        <div className="flex flex-col gap-2 border border-warn bg-warn-wash p-4">
                          <p className="text-small text-ink-2">{t.handover}</p>
                          <code className="font-mono text-row text-ink select-all">
                            {reset.oneTimePassword}
                          </code>
                        </div>
                        <EmailSummary
                          rows={[
                            {
                              login: account.login,
                              emailed: reset.emailed,
                              reason: reset.emailSkippedReason,
                            },
                          ]}
                          perMinute={sendingRate(mail)}
                          dict={dict}
                          locale={locale}
                        />
                      </div>
                    ) : null}
                  </>
                )}
              </ActionForm>
            ) : null}
          </div>
        </Panel>
      ) : null}
    </div>
  );
}
