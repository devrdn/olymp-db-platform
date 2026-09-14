"use client";

import { startTransition, useActionState, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip } from "@/components/ui/tooltip";
import type { Account, Role } from "@/lib/api/accounts";
import type { Dictionary } from "@/lib/i18n/dictionary";

import {
  blockAction,
  deleteAction,
  replaceRolesAction,
  resetPasswordAction,
  restoreAction,
  unblockAction,
  unlockSignInAction,
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

/**
 * A titled block. `help` is what the block is for, behind a "?" beside the
 * heading; `hint` is a consequence worth reading before acting, and stays on
 * screen under it.
 */
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

function Outcome({
  state,
  dict,
  doneLabel,
}: {
  state: AccountState;
  dict: Dictionary;
  /** What success says, when "Saved" is not the right word for it. */
  doneLabel?: string;
}) {
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
        {doneLabel ?? dict.accounts.card.saved}
      </p>
    );
  }
  return null;
}

/**
 * A reason field that refuses to submit empty or whitespace-only, the same
 * rule the bulk block and delete dialogs enforce (`selection.tsx`'s
 * `StatusForm`). `noValidate` on the enclosing form keeps the browser's own
 * unstyled, unlocalised validation bubble from ever firing — this check, and
 * the message it shows, are what decide whether the request leaves.
 */
function ReasonField({
  id,
  label,
  missing,
  onChange,
}: {
  id: string;
  label: string;
  missing: boolean;
  onChange: (empty: boolean) => void;
}) {
  return (
    <div className="flex flex-col gap-2">
      <label htmlFor={id} className="font-mono text-label text-ink-3 uppercase">
        {label}
      </label>
      <Textarea
        id={id}
        name="reason"
        required
        aria-invalid={missing || undefined}
        className="min-h-24 font-sans text-body"
        onChange={(event) => onChange(event.currentTarget.value.trim() === "")}
      />
    </div>
  );
}

/**
 * Clearing a sign-in lockout, behind a confirmation.
 *
 * It is confirmed because it cannot tell the owner from whoever was guessing:
 * both get their attempts back. The confirmation says so, and names the login,
 * so it is not pressed on the wrong card. The request is dispatched from the
 * confirming button rather than a form inside the dialog, which unmounts the
 * moment it closes.
 */
function UnlockSignIn({ account, dict }: { account: Account; dict: Dictionary }) {
  const t = dict.accounts.card;
  const [open, setOpen] = useState(false);
  const [state, run, pending] = useActionState<AccountState, FormData>(unlockSignInAction, {});

  return (
    <div className="flex flex-col gap-2.5">
      <p className="max-w-body text-small text-ink-2">{t.unlockSignInNote}</p>
      <div className="flex flex-wrap items-center gap-4">
        <Button type="button" variant="secondary" disabled={pending} onClick={() => setOpen(true)}>
          {pending ? t.saving : t.unlockSignIn}
        </Button>
        <Outcome state={state} dict={dict} doneLabel={t.unlocked} />
      </div>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={dict.accounts.selection.bulk.close}>
          <DialogHeader>
            <DialogTitle>{t.unlockSignInConfirmTitle.replace("{login}", account.login)}</DialogTitle>
            <DialogDescription>{t.unlockSignInConfirmBody}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="secondary" onClick={() => setOpen(false)}>
              {t.unlockSignInCancel}
            </Button>
            <Button
              type="button"
              onClick={() => {
                const form = new FormData();
                form.set("userId", account.id);
                setOpen(false);
                startTransition(() => run(form));
              }}
            >
              {t.unlockSignInConfirm}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

export function AccountCard({
  account,
  roles,
  viewerId,
  dict,
  /** The status-change moment, already formatted for the active locale. */
  statusChangedAtLabel,
}: {
  account: Account;
  roles: Role[];
  /** Who is looking, so the screen does not offer them a self-block. */
  viewerId: string;
  dict: Dictionary;
  statusChangedAtLabel: string | null;
}) {
  const t = dict.accounts.card;
  const offered = offeredActions(account, viewerId);

  const [profile, saveProfile, savingProfile] = useActionState(updateProfileAction, {});
  const [roleState, saveRoles, savingRoles] = useActionState(replaceRolesAction, {});
  const [access, changeAccess, changingAccess] = useActionState<AccountState, FormData>(
    account.status === "active" ? blockAction : unblockAction,
    {},
  );
  const [deleteState, runDelete, deleting] = useActionState<AccountState, FormData>(
    deleteAction,
    {},
  );
  const [restoreState, runRestore, restoring] = useActionState<AccountState, FormData>(
    restoreAction,
    {},
  );
  const [reset, resetPassword, resetting] = useActionState<ResetState, FormData>(
    resetPasswordAction,
    {},
  );

  const [blockReasonMissing, setBlockReasonMissing] = useState(false);
  const [deleteReasonMissing, setDeleteReasonMissing] = useState(false);

  const offersDanger =
    offered.block ||
    offered.unblock ||
    offered.delete ||
    offered.restore ||
    offered.resetPassword ||
    offered.unlockSignIn;

  return (
    <div className="flex flex-col gap-8">
      {/* Only when there is something to account for: an account nobody has
          ever blocked or deleted carries an empty statusReason (see
          `accountSchema` in `lib/api/accounts.ts`), and showing this panel
          for it would be an empty frame around nothing. */}
      {account.statusReason ? (
        <Panel title={t.statusTitle}>
          <div className="flex flex-col gap-2">
            <p className="max-w-body text-body text-ink">{account.statusReason}</p>
            <p className="text-small text-ink-3">
              {/* A row backfilled without a timestamp still names the actor —
                  the sentence just drops its second half rather than leaving
                  the punctuation stranded around an empty date ("Changed by
                  X, ."). */}
              {statusChangedAtLabel
                ? t.changedBy
                    .replace("{name}", account.statusChangedByLogin || t.unknownActor)
                    .replace("{date}", statusChangedAtLabel)
                : t.changedByNoDate.replace("{name}", account.statusChangedByLogin || t.unknownActor)}
            </p>
          </div>
        </Panel>
      ) : null}

      {offered.profile ? (
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
      ) : null}

      {offered.roles ? (
        <Panel title={t.roles} hint={t.rolesHint} help={t.rolesHelp} helpLabel={dict.chrome.helpLabel}>
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
      ) : null}

      {offersDanger ? (
        <Panel title={t.danger}>
          <div className="flex flex-col gap-6">
            {offered.block || offered.unblock ? (
              <form
                action={changeAccess}
                onSubmit={(event) => {
                  // Unblocking needs no justification — only a block does.
                  if (!offered.block) return;
                  const reason = String(new FormData(event.currentTarget).get("reason") ?? "").trim();
                  if (reason === "") {
                    event.preventDefault();
                    setBlockReasonMissing(true);
                  } else {
                    setBlockReasonMissing(false);
                  }
                }}
                className="flex flex-col gap-2.5"
                noValidate
              >
                <input type="hidden" name="userId" value={account.id} />
                <p className="max-w-body text-small text-ink-2">{t.blockNote}</p>

                {offered.block ? (
                  <ReasonField
                    id="block-reason"
                    label={t.reasonLabel}
                    missing={blockReasonMissing}
                    onChange={(empty) => {
                      if (blockReasonMissing && !empty) setBlockReasonMissing(false);
                    }}
                  />
                ) : null}

                <div className="flex flex-wrap items-center gap-4">
                  <Button type="submit" variant="danger" disabled={changingAccess}>
                    {offered.block ? t.block : t.unblock}
                  </Button>
                  {blockReasonMissing ? (
                    <p role="alert" className="text-small text-bad">
                      {dict.errors.reason_required}
                    </p>
                  ) : (
                    <Outcome state={access} dict={dict} />
                  )}
                </div>
              </form>
            ) : null}

            {offered.delete ? (
              <form
                action={runDelete}
                onSubmit={(event) => {
                  const reason = String(new FormData(event.currentTarget).get("reason") ?? "").trim();
                  if (reason === "") {
                    event.preventDefault();
                    setDeleteReasonMissing(true);
                  } else {
                    setDeleteReasonMissing(false);
                  }
                }}
                className="flex flex-col gap-2.5"
                noValidate
              >
                <input type="hidden" name="userId" value={account.id} />
                <p className="max-w-body text-small text-ink-2">{t.deleteNote}</p>

                <ReasonField
                  id="delete-reason"
                  label={t.reasonLabel}
                  missing={deleteReasonMissing}
                  onChange={(empty) => {
                    if (deleteReasonMissing && !empty) setDeleteReasonMissing(false);
                  }}
                />

                <div className="flex flex-wrap items-center gap-4">
                  <Button type="submit" variant="danger" disabled={deleting}>
                    {deleting ? t.saving : t.delete}
                  </Button>
                  {deleteReasonMissing ? (
                    <p role="alert" className="text-small text-bad">
                      {dict.errors.reason_required}
                    </p>
                  ) : (
                    <Outcome state={deleteState} dict={dict} />
                  )}
                </div>
              </form>
            ) : null}

            {offered.restore ? (
              <form action={runRestore} className="flex flex-col gap-2.5">
                <input type="hidden" name="userId" value={account.id} />
                <p className="max-w-body text-small text-ink-2">{t.restoreNote}</p>
                <div className="flex flex-wrap items-center gap-4">
                  <Button type="submit" variant="secondary" disabled={restoring}>
                    {restoring ? t.saving : t.restore}
                  </Button>
                  <Outcome state={restoreState} dict={dict} />
                </div>
              </form>
            ) : null}

            {offered.unlockSignIn ? <UnlockSignIn account={account} dict={dict} /> : null}

            {offered.resetPassword ? (
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
            ) : null}
          </div>
        </Panel>
      ) : null}
    </div>
  );
}
