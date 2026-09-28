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
 * Registering an account from the register itself — the last thing this
 * screen could not do. Everything else `/users` already offered (search,
 * filter, block, delete, roles, password reset, in bulk and one at a time)
 * assumed the account already existed; there was no form for one person and
 * no roster import, so an administrator's only way in was a hand-written
 * request.
 *
 * Two dialogs, one trigger row: `NewAccountForm` for the one name an
 * administrator has in mind right now, `ImportRosterForm` for the group a
 * department hands over as a list — the same split `people-panels.tsx`
 * draws between `AddOneParticipant` and `ImportParticipants`, and for the
 * same reason: reaching for the wrong one costs nothing, but a screen
 * offering both without saying which is which is the confusion the separate
 * headings exist to close.
 *
 * A one-time password is the one thing on this screen that is genuinely
 * unrecoverable — lost, the account has to be reset — so both dialogs follow
 * `selection.tsx`'s own answer to that (`ActionDialog` + `ResetPasswordForm`)
 * rather than inventing a second way: `dismissible` stays false, decided by
 * the form itself through `reportDismissible`, for as long as a request is
 * pending or a result with a password on it is on screen. Escape, an outside
 * click and the corner X all route through the same `Dialog`/`DialogContent`
 * `dismissible` prop that solved this the first time.
 */

/**
 * A trigger button and the dialog it opens, closed by default.
 *
 * A near-duplicate of `ActionDialog` in `selection.tsx`, which this file does
 * not import: that component is not exported, kept private to the selection
 * bar's own dialogs, and the two features share nothing else that would
 * justify reaching across them for one component. `dismissible` is decided
 * by the form inside, through `reportDismissible` — only it knows whether a
 * request is pending or a result the administrator must acknowledge is on
 * screen.
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
      {/* keepMounted defaults to false, so the form inside — and the
          useActionState it holds — starts fresh every time this reopens
          rather than showing the last run's result. */}
      <Dialog open={open} onOpenChange={setOpen} dismissible={dismissible}>
        <DialogContent closeLabel={closeLabel} dismissible={dismissible}>
          {children(() => setOpen(false), setDismissible)}
        </DialogContent>
      </Dialog>
    </>
  );
}

/** The role catalogue, as a list of checkboxes — the same control the bulk
 * roles dialog and the single-account card use. Nothing renders when the
 * installation has no roles to offer, which is the honest empty case rather
 * than a heading over nothing. */
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

/** One issued password, with a way to copy it — the same row
 * `selection.tsx`'s `IssuedRow` renders for a bulk password reset. */
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
            // Clipboard access can be refused (insecure origin, no
            // permission). The password stays on screen and selectable by
            // hand either way — see `select-all` above.
          }
        }}
      >
        {copied ? copiedLabel : copyLabel}
      </Button>
    </li>
  );
}

/** Every row a roster import declined, named by login with its reason in the
 * interface's own words. `reason` is looked up in the closed vocabulary this
 * build translates (`accounts.create.roster.reason`, mirroring
 * `IMPORT_SKIP_REASONS`) and shown raw when the lookup misses — a reason a
 * newer server has shipped is still an outcome the administrator has to see. */
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

/** The rows an import stopped before reaching — nothing was wrong with them,
 * the server stopped (`stopped` names why) — so the administrator can paste
 * exactly those lines again. Shown apart from the skipped rows, which are
 * lines to fix. */
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

  // While a request is in flight, or while the password it issued is still
  // on screen, Escape, an outside click and the corner X must not be able to
  // discard it — only the explicit Done below can.
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
                // Every login and its password, one pair per line — the same
                // shape a spreadsheet or a mail merge can paste from
                // directly, so handing over thirty passwords is one paste
                // rather than thirty individual copies.
                const lines = created
                  .map((row) => `${row.user.login}\t${row.one_time_password}`)
                  .join("\n");
                try {
                  await navigator.clipboard.writeText(lines);
                  setCopiedAll(true);
                } catch {
                  // Same tolerance as every other copy control here: the
                  // list stays on screen and selectable by hand either way.
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

/**
 * The two entry points: a trigger for one account, a trigger for a roster.
 *
 * `roles` is the catalogue the server publishes (already fetched beside the
 * register for `SelectionBar`), so both dialogs offer exactly what the
 * single-account card and the bulk roles dialog do, nothing this build has
 * to keep in step by hand.
 */
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
