"use client";

import { useActionState, useEffect, useState } from "react";

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
import type { Role } from "@/lib/api/accounts";
import type { Dictionary } from "@/lib/i18n/dictionary";

import {
  createAccountAction,
  importAccountsAction,
  type CreateAccountState,
  type ImportAccountsState,
} from "./create-actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * Creating accounts from the register: one account (`NewAccountForm`) or a
 * roster (`ImportRosterForm`).
 *
 * A one-time password cannot be recovered, so, as in `selection.tsx`, each form
 * keeps its dialog undismissable while a request is pending or a password is on
 * screen.
 */

/**
 * A trigger and its dialog. Mirrors the private `ActionDialog` in
 * `selection.tsx`; the form decides `dismissible` through `reportDismissible`.
 */
function TriggerDialog({
  triggerLabel,
  closeLabel,
  children,
}: {
  triggerLabel: string;
  closeLabel: string;
  children: (close: () => void, reportDismissible: (value: boolean) => void) => React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [dismissible, setDismissible] = useState(true);

  return (
    <>
      <Button
        type="button"
        variant="secondary"
        size="sm"
        onClick={() => {
          setDismissible(true);
          setOpen(true);
        }}
      >
        {triggerLabel}
      </Button>
      {/* Content mounts only while open, so the form and its `useActionState`
         start fresh on every reopen. */}
      <Dialog open={open} onOpenChange={setOpen} dismissible={dismissible}>
        <DialogContent closeLabel={closeLabel} dismissible={dismissible}>
          {children(() => setOpen(false), setDismissible)}
        </DialogContent>
      </Dialog>
    </>
  );
}

/** The role catalogue as checkboxes; nothing renders when there are no roles. */
function RolesPicker({ roles, label }: { roles: Role[]; label: string }) {
  if (roles.length === 0) return null;
  return (
    <div className="flex flex-col gap-2">
      <span className="font-mono text-label text-ink-3 uppercase">{label}</span>
      <div className="flex max-h-40 flex-col gap-2.5 overflow-y-auto">
        {roles.map((role) => (
          <label key={role.code} className="flex items-center gap-2.5 text-control text-ink">
            <input type="checkbox" name="roles" value={role.code} className="size-4 accent-cta" />
            {role.name}
            <span className="font-mono text-data text-ink-3">{role.code}</span>
          </label>
        ))}
      </div>
    </div>
  );
}

/** One issued password with a copy button. */
function PasswordRow({
  login,
  password,
  copyLabel,
  copiedLabel,
}: {
  login: string;
  password: string;
  copyLabel: string;
  copiedLabel: string;
}) {
  const [copied, setCopied] = useState(false);

  return (
    <li className="flex flex-wrap items-center justify-between gap-3 border border-warn bg-warn-wash p-3">
      <div className="flex flex-col gap-1">
        <span className="font-mono text-data text-ink-3">{login}</span>
        <code className="font-mono text-row text-ink select-all">{password}</code>
      </div>
      <Button
        type="button"
        variant="quiet"
        size="sm"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(password);
            setCopied(true);
          } catch {
            // Clipboard access can be refused; the password stays selectable on
            // screen.
          }
        }}
      >
        {copied ? copiedLabel : copyLabel}
      </Button>
    </li>
  );
}

/**
 * Rows an import declined, by login with the reason; an unknown reason from a
 * newer server is shown raw.
 */
function SkippedRows({
  skipped,
  reasons,
}: {
  skipped: readonly { login: string; reason: string }[];
  reasons: Record<string, string>;
}) {
  if (skipped.length === 0) return null;
  return (
    <ul className="flex max-h-48 flex-col gap-1 overflow-y-auto border-t border-line pt-3">
      {skipped.map((row, index) => (
        <li key={`${row.login}-${index}`} className="font-mono text-data text-ink-2">
          {row.login}
          <span className="ml-3 text-warn">{reasons[row.reason] ?? row.reason}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * Rows the import never reached (the server stopped, `stopped` says why),
 * listed so they can be pasted again. Separate from skipped rows, which need
 * fixing.
 */
function NotImportedRows({ logins, title, why }: { logins: readonly string[]; title: string; why: string | null }) {
  return (
    <div role="alert" className="flex flex-col gap-1 border-t border-line pt-3">
      <p className="text-small text-warn">{title}</p>
      {why ? <p className="text-small text-ink-2">{why}</p> : null}
      <ul className="flex max-h-48 flex-col gap-1 overflow-y-auto">
        {logins.map((login, index) => (
          <li key={`${login}-${index}`} className="font-mono text-data text-ink-2">
            {login}
          </li>
        ))}
      </ul>
    </div>
  );
}

function NewAccountForm({
  roles,
  dict,
  onClose,
  reportDismissible,
}: {
  roles: Role[];
  dict: Dictionary;
  onClose: () => void;
  reportDismissible: (value: boolean) => void;
}) {
  const t = dict.accounts.create;
  const [state, formAction, pending] = useActionState<CreateAccountState, FormData>(
    createAccountAction,
    {},
  );

  // While pending or showing a password, only Done may close the dialog.
  useEffect(() => {
    reportDismissible(!pending && !state.result);
  }, [pending, state.result, reportDismissible]);

  if (state.result) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>{t.one.createdTitle}</DialogTitle>
          <DialogDescription>{t.one.handover}</DialogDescription>
        </DialogHeader>

        <ul className="flex flex-col gap-2">
          <PasswordRow
            login={state.result.user.login}
            password={state.result.one_time_password}
            copyLabel={t.roster.copy}
            copiedLabel={t.roster.copied}
          />
        </ul>

        <DialogFooter>
          <Button type="button" onClick={onClose}>
            {t.done}
          </Button>
        </DialogFooter>
      </>
    );
  }

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
    : null;

  return (
    <form action={formAction} className="flex flex-col gap-5">
      <DialogHeader>
        <DialogTitle>{t.one.title}</DialogTitle>
      </DialogHeader>

      <Field id="create-account-login" label={t.one.login}>
        <Input name="login" required />
      </Field>

      <Field id="create-account-full-name" label={t.one.fullName}>
        <Input name="full_name" required />
      </Field>

      <Field id="create-account-email" label={t.one.email}>
        <Input name="email" type="email" />
      </Field>

      <RolesPicker roles={roles} label={t.one.rolesLabel} />

      {failure ? (
        <p role="alert" className="text-small text-bad">
          {failure}
        </p>
      ) : null}

      <DialogFooter>
        <Button type="button" variant="quiet" onClick={onClose} disabled={pending}>
          {t.cancel}
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? t.one.creating : t.one.submit}
        </Button>
      </DialogFooter>
    </form>
  );
}

function ImportRosterForm({
  roles,
  dict,
  onClose,
  reportDismissible,
}: {
  roles: Role[];
  dict: Dictionary;
  onClose: () => void;
  reportDismissible: (value: boolean) => void;
}) {
  const t = dict.accounts.create;
  const [state, formAction, pending] = useActionState<ImportAccountsState, FormData>(
    importAccountsAction,
    {},
  );
  const [copiedAll, setCopiedAll] = useState(false);

  useEffect(() => {
    reportDismissible(!pending && !state.result);
  }, [pending, state.result, reportDismissible]);

  if (state.result) {
    const created = state.result.created;
    const skipped = state.result.skipped;
    const notImported = state.result.not_imported ?? [];
    const stopped = state.result.stopped;
    const reasons = t.roster.reason as Record<string, string>;

    return (
      <>
        <DialogHeader>
          <DialogTitle>{t.roster.createdTitle}</DialogTitle>
          <DialogDescription>{t.roster.handover}</DialogDescription>
        </DialogHeader>

        <p role="status" className="flex flex-wrap items-baseline gap-x-2 text-small text-ink-2">
          <span className="text-good">{t.roster.created.replace("{n}", String(created.length))}</span>
          {skipped.length > 0 ? (
            <span className="text-warn">· {t.roster.skipped.replace("{n}", String(skipped.length))}</span>
          ) : null}
        </p>

        {created.length > 0 ? (
          <>
            <ul className="flex max-h-64 flex-col gap-2 overflow-y-auto">
              {created.map((row) => (
                <PasswordRow
                  key={row.user.id}
                  login={row.user.login}
                  password={row.one_time_password}
                  copyLabel={t.roster.copy}
                  copiedLabel={t.roster.copied}
                />
              ))}
            </ul>
            <Button
              type="button"
              variant="quiet"
              size="sm"
              className="self-start"
              onClick={async () => {
                // One login and password per line, ready to paste into a
                // spreadsheet or mail merge.
                const lines = created
                  .map((row) => `${row.user.login}\t${row.one_time_password}`)
                  .join("\n");
                try {
                  await navigator.clipboard.writeText(lines);
                  setCopiedAll(true);
                } catch {
                  // The list stays selectable if the clipboard is refused.
                }
              }}
            >
              {copiedAll ? t.roster.copiedAll : t.roster.copyAll}
            </Button>
          </>
        ) : null}

        <SkippedRows skipped={skipped} reasons={reasons} />

        {notImported.length > 0 ? (
          <NotImportedRows
            logins={notImported}
            title={t.roster.notImported.replace("{n}", String(notImported.length))}
            why={stopped ? (messageForCode(stopped, dict.errors)) : null}
          />
        ) : null}

        {created.length === 0 && skipped.length === 0 && notImported.length === 0 ? (
          <p className="text-small text-ink-2">{t.roster.none}</p>
        ) : null}

        <DialogFooter>
          <Button type="button" onClick={onClose}>
            {t.done}
          </Button>
        </DialogFooter>
      </>
    );
  }

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
    : null;

  return (
    <form action={formAction} className="flex flex-col gap-5">
      <DialogHeader>
        <DialogTitle>{t.roster.title}</DialogTitle>
        <DialogDescription>{t.roster.hint}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-2">
        <label
          htmlFor="create-account-roster"
          className="font-mono text-label text-ink-3 uppercase"
        >
          {t.roster.rosterLabel}
        </label>
        <Textarea
          id="create-account-roster"
          name="roster"
          required
          className="min-h-32 font-mono text-data"
          placeholder={t.roster.placeholder}
        />
      </div>

      <RolesPicker roles={roles} label={t.roster.rolesLabel} />

      {failure ? (
        <p role="alert" className="text-small text-bad">
          {failure}
        </p>
      ) : null}

      <DialogFooter>
        <Button type="button" variant="quiet" onClick={onClose} disabled={pending}>
          {t.cancel}
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? t.roster.importing : t.roster.submit}
        </Button>
      </DialogFooter>
    </form>
  );
}

/** Triggers for one account and for a roster. `roles` is the server's catalogue. */
export function AccountCreateControls({ roles, dict }: { roles: Role[]; dict: Dictionary }) {
  const t = dict.accounts.create;

  return (
    <div className="flex flex-wrap items-center gap-2">
      <TriggerDialog triggerLabel={t.new} closeLabel={t.close}>
        {(close, reportDismissible) => (
          <NewAccountForm roles={roles} dict={dict} onClose={close} reportDismissible={reportDismissible} />
        )}
      </TriggerDialog>
      <TriggerDialog triggerLabel={t.import} closeLabel={t.close}>
        {(close, reportDismissible) => (
          <ImportRosterForm
            roles={roles}
            dict={dict}
            onClose={close}
            reportDismissible={reportDismissible}
          />
        )}
      </TriggerDialog>
    </div>
  );
}
