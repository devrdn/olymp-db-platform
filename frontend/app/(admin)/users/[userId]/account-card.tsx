"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import type { Account, Role } from "@/lib/api/accounts";
import type { Dictionary } from "@/lib/i18n/dictionary";

import {
  blockAction,
  replaceRolesAction,
  resetPasswordAction,
  unblockAction,
  updateProfileAction,
  type AccountState,
  type ResetState,
} from "./actions";
import { offeredActions } from "./offered";

/**
 * One account, and what an administrator may do to it.
 *
 * Three forms, not one, because they are three endpoints: the descriptive
 * fields, the role set, and access. A single save would send all three on
 * every change, and a refusal from one would discard the other two — the same
 * reasoning the contest settings panels carry.
 *
 * That is not in tension with the single Save the questions page is getting
 * (architecture 6.3). There the three requests describe *one* object and the
 * fix is one endpoint taking it whole; here they are three genuinely separate
 * decisions, and blocking somebody is not a thing to do by accident while
 * correcting the spelling of their name.
 */

function Panel({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-5 border-t border-line pt-5">
      <div className="flex flex-col gap-1.5">
        <h3 className="text-h3 text-ink">{title}</h3>
        {hint ? <p className="max-w-body text-small text-ink-2">{hint}</p> : null}
      </div>
      {children}
    </section>
  );
}

function Outcome({ state, dict }: { state: AccountState; dict: Dictionary }) {
  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  if (failure) {
    return (
      <p role="alert" className="max-w-body text-small text-bad">
        {failure}
      </p>
    );
  }
  if (state.done) {
    return (
      <p role="status" className="text-small text-good">
        {dict.accounts.card.saved}
      </p>
    );
  }
  return null;
}

export function AccountCard({
  account,
  roles,
  viewerId,
  dict,
}: {
  account: Account;
  roles: Role[];
  /** Who is looking, so the screen does not offer them a self-block. */
  viewerId: string;
  dict: Dictionary;
}) {
  const t = dict.accounts.card;
  const offered = offeredActions(account, viewerId);

  const [profile, saveProfile, savingProfile] = useActionState(updateProfileAction, {});
  const [roleState, saveRoles, savingRoles] = useActionState(replaceRolesAction, {});
  const [access, changeAccess, changingAccess] = useActionState<AccountState, FormData>(
    account.status === "active" ? blockAction : unblockAction,
    {},
  );
  const [reset, resetPassword, resetting] = useActionState<ResetState, FormData>(
    resetPasswordAction,
    {},
  );

  return (
    <div className="flex flex-col gap-8">
      <Panel title={t.profile}>
        {/* Keyed on what the server last returned, so a saved value replaces
            what was typed rather than the field keeping a stale draft. */}
        <form key={account.fullName + account.email} action={saveProfile} className="flex flex-col gap-5">
          <input type="hidden" name="userId" value={account.id} />

          <Field id="full_name" label={t.fullName}>
            <Input name="full_name" defaultValue={account.fullName} required />
          </Field>

          <Field id="email" label={t.email}>
            <Input name="email" type="email" defaultValue={account.email ?? ""} />
          </Field>

          <div className="flex flex-wrap items-center gap-4">
            <Button type="submit" disabled={savingProfile}>
              {savingProfile ? t.saving : t.save}
            </Button>
            <Outcome state={profile} dict={dict} />
          </div>
        </form>
      </Panel>

      <Panel title={t.roles} hint={t.rolesHint}>
        <form key={account.roles.join(",")} action={saveRoles} className="flex flex-col gap-5">
          <input type="hidden" name="userId" value={account.id} />

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

          <div className="flex flex-wrap items-center gap-4">
            <Button type="submit" disabled={savingRoles}>
              {savingRoles ? t.saving : t.save}
            </Button>
            <Outcome state={roleState} dict={dict} />
          </div>
        </form>
      </Panel>

      <Panel title={t.danger}>
        <div className="flex flex-col gap-6">
          {offered.block || offered.unblock ? (
            <form action={changeAccess} className="flex flex-col gap-2.5">
              <input type="hidden" name="userId" value={account.id} />
              <p className="max-w-body text-small text-ink-2">{t.blockNote}</p>
              <div className="flex flex-wrap items-center gap-4">
                <Button type="submit" variant="danger" disabled={changingAccess}>
                  {offered.block ? t.block : t.unblock}
                </Button>
                <Outcome state={access} dict={dict} />
              </div>
            </form>
          ) : null}

          <form action={resetPassword} className="flex flex-col gap-2.5">
            <input type="hidden" name="userId" value={account.id} />
            <p className="max-w-body text-small text-ink-2">{t.resetNote}</p>
            <div className="flex flex-wrap items-center gap-4">
              <Button type="submit" variant="secondary" disabled={resetting}>
                {resetting ? t.saving : t.resetPassword}
              </Button>
              <Outcome state={{ code: reset.code }} dict={dict} />
            </div>

            {/* Shown until the administrator leaves the page, not flashed in a
                toast: it arrives exactly once and cannot be retrieved again,
                so a glance that misses it costs another reset. */}
            {reset.oneTimePassword ? (
              <div className="mt-2 flex max-w-body flex-col gap-2 border border-warn bg-warn-wash p-4">
                <p className="text-small text-ink-2">{t.handover}</p>
                <code className="font-mono text-row text-ink select-all">
                  {reset.oneTimePassword}
                </code>
              </div>
            ) : null}
          </form>
        </div>
      </Panel>
    </div>
  );
}
