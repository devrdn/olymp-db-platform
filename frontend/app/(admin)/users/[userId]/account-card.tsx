"use client";

import { ActionButton, ActionFooter, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Tooltip } from "@/components/ui/tooltip";
import type { Account, Role } from "@/lib/api/accounts";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill, isolate } from "@/lib/i18n/fill";

import {
  blockAction,
  deleteAction,
  replaceRolesAction,
  resetPasswordAction,
  restoreAction,
  unblockAction,
  unlockSignInAction,
  updateProfileAction,
} from "./actions";
import { EmailSummary, SendByEmailOption, sendingRate, type MailOffer } from "../email-delivery";
import { ReasonField, useRequiredReason } from "../required-reason";
import { offeredActions } from "./offered";

/**
 * One account and its actions. Three forms for three endpoints (profile, roles,
 * access): these are separate decisions, and a typo fix must not risk a block.
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
  return (
    <section className="flex flex-col gap-5 border-t border-line pt-5">
      <div className="flex flex-col gap-1.5">
        <div className="flex items-center gap-2">
          <h3 className="text-h3 text-ink">{title}</h3>
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

export function AccountCard({
  account,
  roles,
  viewerId,
  dict,
  /** Already formatted for the locale. */
  statusChangedAtLabel,
  locale,
  mail,
}: {
  account: Account;
  roles: Role[];
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
