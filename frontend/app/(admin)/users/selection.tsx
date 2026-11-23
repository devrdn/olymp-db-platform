"use client";

import {
  createContext,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
} from "react";

import { X } from "lucide-react";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { Button, buttonVariants } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import type { IssuedPassword, Role, SkippedAccount } from "@/lib/api/accounts";
import { MAX_BULK_ACCOUNTS } from "@/lib/api/accounts-terms";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  bulkBlockAction,
  bulkDeleteAction,
  bulkReplaceRolesAction,
  bulkResetPasswordAction,
  bulkUnblockAction,
  type BulkState,
} from "./bulk-actions";
import { EmailSummary, SendByEmailOption, sendingRate, type MailOffer } from "./email-delivery";
import { fill } from "@/lib/i18n/fill";
import { labelFor } from "@/lib/i18n/label";

export type SelectedAccount = { id: string; display: string };

/**
 * The administrator's picked accounts. An external store read through
 * `useSyncExternalStore`, not context, so ticking one box re-renders one row
 * rather than every consumer.
 *
 * Held in `layout.tsx` so a pick survives a search, which re-renders only the
 * page. Each id keeps the label captured when it was checked, so an account
 * pushed off screen can still be named in the selection panel.
 */
class SelectionStore {
  private entries = new Map<string, string>();
  private listeners = new Set<() => void>();
  // Snapshots are cached until a mutation: `useSyncExternalStore` compares them
  // on every render, and a fresh array each time would loop.
  private cachedSelected: string[] | null = null;
  private cachedList: SelectedAccount[] | null = null;
  // `undefined` until the first `syncOwner`, distinct from every real owner, so
  // the first sync never clears.
  private owner: string | null | undefined = undefined;

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  has = (id: string) => this.entries.has(id);
  size = () => this.entries.size;

  selected = () => {
    if (this.cachedSelected === null) this.cachedSelected = [...this.entries.keys()];
    return this.cachedSelected;
  };

  /** Every picked account with its captured label. */
  list = () => {
    if (this.cachedList === null) {
      this.cachedList = [...this.entries.entries()].map(([id, display]) => ({ id, display }));
    }
    return this.cachedList;
  };

  toggle = (id: string, display: string) => {
    if (!this.entries.delete(id)) this.entries.set(id, display);
    this.emit();
  };

  /** Adds rows without touching picks from other pages or searches. */
  selectMany = (rows: SelectedAccount[]) => {
    for (const row of rows) this.entries.set(row.id, row.display);
    this.emit();
  };

  /** Removes exactly these ids: "deselect this page" and a row's remove button. */
  deselectMany = (ids: readonly string[]) => {
    for (const id of ids) this.entries.delete(id);
    this.emit();
  };

  /** Drops the whole selection. */
  clear = () => {
    this.entries.clear();
    this.emit();
  };

  /**
   * Clears the selection when the signed-in administrator changes. Next's
   * client router cache is not scoped to the user, so a back navigation after a
   * sign-out and another sign-in could otherwise revive the previous
   * administrator's picks.
   */
  syncOwner = (owner: string | null) => {
    if (this.owner === owner) return;
    this.owner = owner;
    if (this.entries.size === 0) return;
    this.entries.clear();
    this.emit();
  };

  private emit() {
    this.cachedSelected = null;
    this.cachedList = null;
    for (const listener of this.listeners) listener();
  }
}

const EMPTY_SELECTION: readonly string[] = [];
const EMPTY_LIST: readonly SelectedAccount[] = [];

const SelectionContext = createContext<SelectionStore | null>(null);

function useSelectionStore(): SelectionStore {
  const store = useContext(SelectionContext);
  if (!store) {
    throw new Error("useSelectionStore must be used within a SelectionProvider");
  }
  return store;
}

/**
 * Provides the store. The instance travels through context because it never
 * changes identity; the selected set must not go through context, or every
 * consumer would re-render on each tick. `owner` is the administrator id
 * `layout.tsx` reads per request (see `SelectionStore.syncOwner`); it
 * defaults to `null` for tests.
 */
export function SelectionProvider({
  owner = null,
  children,
}: {
  owner?: string | null;
  children: React.ReactNode;
}) {
  const [store] = useState(() => new SelectionStore());
  useEffect(() => {
    store.syncOwner(owner);
  }, [store, owner]);
  return <SelectionContext.Provider value={store}>{children}</SelectionContext.Provider>;
}

export function useSelectedIds(): readonly string[] {
  const store = useSelectionStore();
  return useSyncExternalStore(store.subscribe, store.selected, () => EMPTY_SELECTION);
}

/**
 * One row's box, subscribed only to its own membership. `display` names the
 * account in the selection panel (defaults to `id`). The server snapshot is
 * `false` because the server never knows the selection; anything else is a
 * hydration mismatch.
 */
export function RowCheckbox({
  id,
  label,
  display = id,
}: {
  id: string;
  label: string;
  display?: string;
}) {
  const store = useSelectionStore();
  const checked = useSyncExternalStore(store.subscribe, () => store.has(id), () => false);

  return (
    <Checkbox
      checked={checked}
      onCheckedChange={() => store.toggle(id, display)}
      aria-label={label}
    />
  );
}

/**
 * The header box for the visible page, showing the mixed state when some are
 * picked. Uses `selectMany`/`deselectMany`, never `clear`, so it never reaches
 * picks on other pages.
 */
export function SelectAllCheckbox({
  ids,
  displays,
  label,
}: {
  ids: string[];
  /** id -> display label, used when picking; falls back to the id. */
  displays?: Record<string, string>;
  label: string;
}) {
  const store = useSelectionStore();
  const pickedHere = useSyncExternalStore(
    store.subscribe,
    () => ids.filter((id) => store.has(id)).length,
    () => 0,
  );
  const allPicked = ids.length > 0 && pickedHere === ids.length;
  const somePicked = pickedHere > 0 && !allPicked;

  return (
    <Checkbox
      checked={allPicked}
      indeterminate={somePicked}
      onCheckedChange={() =>
        allPicked
          ? store.deselectMany(ids)
          : store.selectMany(ids.map((id) => ({ id, display: displays?.[id] ?? id })))
      }
      aria-label={label}
    />
  );
}

/**
 * Accounts a bulk operation declined, by login with the reason. Never hidden,
 * since each was selected deliberately. An unknown reason from a newer server
 * is shown raw.
 */
function SkippedList({ skipped, dict }: { skipped: readonly SkippedAccount[]; dict: Dictionary }) {
  if (skipped.length === 0) return null;
  const reasons = dict.accounts.selection.bulk.reason;

  return (
    <ul className="flex max-h-48 flex-col gap-1 overflow-y-auto border-t border-line pt-3">
      {skipped.map((row) => (
        <li key={row.id} className="font-mono text-data text-ink-2">
          {/* A not_found row carries no login. */}
          {row.login || row.id}
          <span className="ml-3 text-warn">{labelFor(reasons, row.reason) ?? row.reason}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * Names every selected account, with a remove button each. Picks can span
 * searches, so a count alone would hide accounts a destructive bulk action will
 * touch.
 */
function SelectionList({ dict }: { dict: Dictionary }) {
  const store = useSelectionStore();
  const rows = useSyncExternalStore(store.subscribe, store.list, () => EMPTY_LIST);
  const t = dict.accounts.selection;

  if (rows.length === 0) return null;

  return (
    <ul className="flex max-h-64 flex-col gap-1 overflow-y-auto border-t border-line pt-3">
      {rows.map((row) => (
        <li
          key={row.id}
          className="flex items-center justify-between gap-3 font-mono text-data text-ink-2"
        >
          <span className="truncate">{row.display}</span>
          <button
            type="button"
            onClick={() => store.deselectMany([row.id])}
            className="shrink-0 text-ink-3 transition-colors duration-(--t-input) ease-standard hover:text-ink"
          >
            <X className="size-3.5" />
            <span className="sr-only">{fill(t.unpick, { name: row.display })}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}

/** Shows who is selected. A plain read of the store, so it stays dismissible. */
function SelectionListDialog({ dict }: { dict: Dictionary }) {
  const [open, setOpen] = useState(false);
  const t = dict.accounts.selection;

  return (
    <>
      <Button type="button" variant="quiet" size="sm" onClick={() => setOpen(true)}>
        {t.view}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t.bulk.close}>
          <DialogHeader>
            <DialogTitle>{t.viewTitle}</DialogTitle>
          </DialogHeader>
          <SelectionList dict={dict} />
        </DialogContent>
      </Dialog>
    </>
  );
}

function ChangedSkipped({
  changedCount,
  skipped,
  dict,
}: {
  changedCount: number;
  skipped: readonly SkippedAccount[];
  dict: Dictionary;
}) {
  const t = dict.accounts.selection.bulk;

  return (
    <div role="status" className="flex flex-col gap-3">
      <p className="text-small text-ink-2">
        <span className="text-good">{fill(t.changed, { n: changedCount })}</span>
        {skipped.length > 0 ? (
          <span className="text-warn"> · {fill(t.skipped, { n: skipped.length })}</span>
        ) : null}
      </p>
      <SkippedList skipped={skipped} dict={dict} />
    </div>
  );
}

function IssuedRow({ row, dict }: { row: IssuedPassword; dict: Dictionary }) {
  const t = dict.accounts.selection.bulk.resetDialog;
  const [copied, setCopied] = useState(false);

  return (
    <li className="flex flex-wrap items-center justify-between gap-3 border border-warn bg-warn-wash p-3">
      <div className="flex flex-col gap-1">
        <span className="font-mono text-data text-ink-3">{row.login}</span>
        <code className="font-mono text-row text-ink select-all">{row.oneTimePassword}</code>
      </div>
      <Button
        type="button"
        variant="quiet"
        size="sm"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(row.oneTimePassword);
            setCopied(true);
          } catch {
            // Clipboard access can be refused; the password stays selectable on
            // screen.
          }
        }}
      >
        {copied ? t.copied : t.copy}
      </Button>
    </li>
  );
}

function ActionDialog({
  triggerLabel,
  triggerVariant,
  disabled,
  closeLabel,
  children,
}: {
  triggerLabel: string;
  triggerVariant: "secondary" | "danger";
  disabled: boolean;
  closeLabel: string;
  // The form reports whether the dialog may be dismissed, since only it knows
  // whether a request is pending or a result is showing.
  children: (close: () => void, reportDismissible: (value: boolean) => void) => React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [dismissible, setDismissible] = useState(true);

  return (
    <>
      <Button
        type="button"
        variant={triggerVariant}
        size="sm"
        disabled={disabled}
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

/** Tells the dialog whether it may be dismissed: not while pending or holding a result. */
function ReportDismissible({
  pending,
  done,
  report,
}: {
  pending: boolean;
  done: boolean;
  report: (value: boolean) => void;
}) {
  useEffect(() => {
    report(!pending && !done);
  }, [pending, done, report]);
  return null;
}

/**
 * Block, unblock and delete: one status change. Block and delete require a
 * reason, checked here before the request leaves (the server refuses an empty
 * one too).
 */
function StatusForm({
  ids,
  dict,
  titleTemplate,
  description,
  requireReason,
  danger,
  action,
  onClose,
  reportDismissible,
}: {
  ids: readonly string[];
  dict: Dictionary;
  titleTemplate: string;
  description: string;
  requireReason: boolean;
  danger: boolean;
  action: (previous: BulkState, form: FormData) => Promise<BulkState>;
  onClose: () => void;
  reportDismissible: (value: boolean) => void;
}) {
  const t = dict.accounts.selection.bulk;
  const store = useSelectionStore();
  // Frozen at mount: a successful run clears the selection, and the outcome on
  // screen must not change under it.
  const [frozenIds] = useState(ids);
  const title = fill(titleTemplate, { n: frozenIds.length });
  const [reasonMissing, setReasonMissing] = useState(false);

  return (
    <ActionForm
      action={action}
      fields={{ ids: frozenIds }}
      errors={dict.errors}
      onSubmit={(event) => {
        if (!requireReason) return;
        const reason = String(new FormData(event.currentTarget).get("reason") ?? "").trim();
        if (reason === "") {
          // An empty or whitespace-only reason never reaches the server.
          event.preventDefault();
          setReasonMissing(true);
        } else {
          setReasonMissing(false);
        }
      }}
      className="flex flex-col gap-5"
      noValidate
    >
      {(state, pending) => (
        <>
          <ReportDismissible pending={pending} done={state.result !== undefined} report={reportDismissible} />
          {state.result ? (
            <>
              <DialogHeader>
                <DialogTitle>{title}</DialogTitle>
              </DialogHeader>
              <ChangedSkipped changedCount={state.result.changed.length} skipped={state.result.skipped} dict={dict} />
              <DialogFooter>
                <Button
                  type="button"
                  onClick={() => {
                    // Kept when nothing changed, so a mistaken pick can be fixed
                    // and retried.
                    if (state.result && state.result.changed.length > 0) store.clear();
                    onClose();
                  }}
                >
                  {t.done}
                </Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{title}</DialogTitle>
                <DialogDescription>{description}</DialogDescription>
              </DialogHeader>

              {requireReason ? (
                <div className="flex flex-col gap-2">
                  <label htmlFor="bulk-reason" className="font-mono text-label text-ink-3 uppercase">
                    {t.reasonLabel}
                  </label>
                  {/* `required` for ARIA only: the form is `noValidate`, so the
                     browser's unlocalised bubble never fires. */}
                  <Textarea
                    id="bulk-reason"
                    name="reason"
                    required
                    aria-invalid={reasonMissing || undefined}
                    className="min-h-24 font-sans text-body"
                    onChange={(event) => {
                      // Clear the error as soon as it is corrected.
                      if (reasonMissing && event.currentTarget.value.trim() !== "") {
                        setReasonMissing(false);
                      }
                    }}
                  />
                </div>
              ) : null}

              <ActionOutcome refusal={reasonMissing ? "reason_required" : undefined} />

              <DialogFooter>
                <Button type="button" variant="quiet" onClick={onClose} disabled={pending}>
                  {t.cancel}
                </Button>
                <ActionButton variant={danger ? "danger" : "primary"} pendingLabel={t.submitting}>
                  {t.confirm}
                </ActionButton>
              </DialogFooter>
            </>
          )}
        </>
      )}
    </ActionForm>
  );
}

/**
 * Replaces the role set on every selected account. An empty set is legitimate
 * but must not be the silent default, so an empty submit asks first, naming
 * the count.
 */
function RolesForm({
  ids,
  roles,
  dict,
  onClose,
  reportDismissible,
}: {
  ids: readonly string[];
  roles: Role[];
  dict: Dictionary;
  onClose: () => void;
  reportDismissible: (value: boolean) => void;
}) {
  const t = dict.accounts.selection.bulk;
  const store = useSelectionStore();
  const [frozenIds] = useState(ids);
  const title = fill(t.rolesDialog.title, { n: frozenIds.length });

  return (
    <ActionForm
      action={bulkReplaceRolesAction}
      fields={{ ids: frozenIds }}
      confirm={(form) =>
        form.getAll("roles").length === 0 ? fill(t.rolesDialog.confirmEmpty, { n: frozenIds.length }) : null
      }
      errors={dict.errors}
      className="flex flex-col gap-5"
    >
      {(state, pending) => (
        <>
          <ReportDismissible pending={pending} done={state.result !== undefined} report={reportDismissible} />
          {state.result ? (
            <>
              <DialogHeader>
                <DialogTitle>{title}</DialogTitle>
              </DialogHeader>
              <ChangedSkipped changedCount={state.result.changed.length} skipped={state.result.skipped} dict={dict} />
              <DialogFooter>
                <Button
                  type="button"
                  onClick={() => {
                    if (state.result && state.result.changed.length > 0) store.clear();
                    onClose();
                  }}
                >
                  {t.done}
                </Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{title}</DialogTitle>
                <DialogDescription>{t.rolesDialog.description}</DialogDescription>
              </DialogHeader>

              <div className="flex max-h-56 flex-col gap-2.5 overflow-y-auto">
                {roles.map((role) => (
                  <label key={role.code} className="flex items-center gap-2.5 text-control text-ink">
                    <input type="checkbox" name="roles" value={role.code} className="size-4 accent-cta" />
                    {role.name}
                    <span className="font-mono text-data text-ink-3">{role.code}</span>
                  </label>
                ))}
              </div>

              <ActionOutcome />

              <DialogFooter>
                <Button type="button" variant="quiet" onClick={onClose} disabled={pending}>
                  {t.cancel}
                </Button>
                <ActionButton pendingLabel={t.submitting}>{t.rolesDialog.submit}</ActionButton>
              </DialogFooter>
            </>
          )}
        </>
      )}
    </ActionForm>
  );
}

/**
 * Issues a one-time password for every selected account after a confirmation,
 * since it ends every session of each account. Each password is shown until
 * dismissed and never retrievable again.
 */
function ResetPasswordForm({
  ids,
  dict,
  locale,
  mail,
  onClose,
  reportDismissible,
}: {
  ids: readonly string[];
  dict: Dictionary;
  locale: Locale;
  mail: MailOffer;
  onClose: () => void;
  reportDismissible: (value: boolean) => void;
}) {
  const t = dict.accounts.selection.bulk;
  const store = useSelectionStore();
  const [frozenIds] = useState(ids);

  return (
    <ActionForm
      action={bulkResetPasswordAction}
      fields={{ ids: frozenIds }}
      errors={dict.errors}
      className="flex flex-col gap-5"
    >
      {(state, pending) => (
        <>
          <ReportDismissible pending={pending} done={state.result !== undefined} report={reportDismissible} />
          {state.result ? (
            <>
              <DialogHeader>
                <DialogTitle>{t.resetDialog.issued}</DialogTitle>
                <DialogDescription>{t.resetDialog.handover}</DialogDescription>
              </DialogHeader>

              {state.result.issued.length > 0 ? (
                <ul className="flex max-h-64 flex-col gap-2 overflow-y-auto">
                  {state.result.issued.map((row) => (
                    <IssuedRow key={row.id} row={row} dict={dict} />
                  ))}
                </ul>
              ) : null}

              <EmailSummary
                rows={state.result.issued.map((row) => ({
                  login: row.login,
                  emailed: row.emailed,
                  reason: row.emailSkippedReason,
                }))}
                perMinute={sendingRate(mail)}
                dict={dict}
                locale={locale}
              />

              <SkippedList skipped={state.result.skipped} dict={dict} />

              {state.result.issued.length === 0 && state.result.skipped.length === 0 ? (
                <p className="text-small text-ink-2">{t.resetDialog.none}</p>
              ) : null}

              <DialogFooter>
                <Button
                  type="button"
                  onClick={() => {
                    if (state.result && state.result.issued.length > 0) store.clear();
                    onClose();
                  }}
                >
                  {t.done}
                </Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{fill(t.resetDialog.title, { n: frozenIds.length })}</DialogTitle>
                <DialogDescription>{t.resetDialog.description}</DialogDescription>
              </DialogHeader>

              <SendByEmailOption mail={mail} dict={dict} />

              <ActionOutcome />

              <DialogFooter>
                <Button type="button" variant="quiet" onClick={onClose} disabled={pending}>
                  {t.cancel}
                </Button>
                <ActionButton pendingLabel={t.submitting}>{t.resetDialog.submit}</ActionButton>
              </DialogFooter>
            </>
          )}
        </>
      )}
    </ActionForm>
  );
}

/**
 * The selection count and the bulk actions. `roles` is the server's catalogue,
 * so the dialog offers what the single-account card does.
 */
export function SelectionBar({
  dict,
  roles,
  pageIds,
  locale,
  mail,
}: {
  dict: Dictionary;
  roles: Role[];
  locale: Locale;
  /** Whether the password reset offers emailing the sign-in details. */
  mail: MailOffer;
  /**
   * Ids on the current page, used only to report how much of the selection is
   * off screen; bulk actions always act on the whole selection.
   */
  pageIds: string[];
}) {
  const store = useSelectionStore();
  const count = useSyncExternalStore(store.subscribe, () => store.size(), () => 0);
  const onThisPage = useSyncExternalStore(
    store.subscribe,
    () => pageIds.filter((id) => store.has(id)).length,
    () => 0,
  );
  const ids = useSelectedIds();
  const t = dict.accounts.selection;
  const bulk = t.bulk;
  // A page is 50 rows, but picks span pages; never send more than the backend
  // accepts.
  const tooMany = count > MAX_BULK_ACCOUNTS;
  // Picks off this page are still touched by a bulk action, so say how many.
  const offPage = count - onThisPage;

  if (count === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-3 border border-line-2 bg-panel px-4 py-2.5">
      <p role="status" aria-live="polite" className="font-mono text-data text-ink-2">
        {fill(t.count, { n: count })}
        {offPage > 0 ? (
          <span className="text-warn"> · {fill(t.offPage, { n: offPage })}</span>
        ) : null}
      </p>

      <SelectionListDialog dict={dict} />

      <div data-slot="selection-actions" className="flex flex-1 flex-wrap items-center gap-2">
        <ActionDialog
          triggerLabel={bulk.block}
          triggerVariant="danger"
          disabled={tooMany}
          closeLabel={bulk.close}
        >
          {(close, reportDismissible) => (
            <StatusForm
              ids={ids}
              dict={dict}
              titleTemplate={bulk.blockDialog.title}
              description={bulk.blockDialog.description}
              requireReason
              danger
              action={bulkBlockAction}
              onClose={close}
              reportDismissible={reportDismissible}
            />
          )}
        </ActionDialog>

        <ActionDialog
          triggerLabel={bulk.unblock}
          triggerVariant="secondary"
          disabled={tooMany}
          closeLabel={bulk.close}
        >
          {(close, reportDismissible) => (
            <StatusForm
              ids={ids}
              dict={dict}
              titleTemplate={bulk.unblockDialog.title}
              description={bulk.unblockDialog.description}
              requireReason={false}
              danger={false}
              action={bulkUnblockAction}
              onClose={close}
              reportDismissible={reportDismissible}
            />
          )}
        </ActionDialog>

        <ActionDialog
          triggerLabel={bulk.delete}
          triggerVariant="danger"
          disabled={tooMany}
          closeLabel={bulk.close}
        >
          {(close, reportDismissible) => (
            <StatusForm
              ids={ids}
              dict={dict}
              titleTemplate={bulk.deleteDialog.title}
              description={bulk.deleteDialog.description}
              requireReason
              danger
              action={bulkDeleteAction}
              onClose={close}
              reportDismissible={reportDismissible}
            />
          )}
        </ActionDialog>

        <ActionDialog
          triggerLabel={bulk.roles}
          triggerVariant="secondary"
          disabled={tooMany}
          closeLabel={bulk.close}
        >
          {(close, reportDismissible) => (
            <RolesForm
              ids={ids}
              roles={roles}
              dict={dict}
              onClose={close}
              reportDismissible={reportDismissible}
            />
          )}
        </ActionDialog>

        <ActionDialog
          triggerLabel={bulk.resetPassword}
          triggerVariant="secondary"
          disabled={tooMany}
          closeLabel={bulk.close}
        >
          {(close, reportDismissible) => (
            <ResetPasswordForm
              ids={ids}
              dict={dict}
              locale={locale}
              mail={mail}
              onClose={close}
              reportDismissible={reportDismissible}
            />
          )}
        </ActionDialog>
      </div>

      {tooMany ? (
        <p role="alert" className="w-full text-small text-bad">
          {fill(bulk.tooMany, { n: MAX_BULK_ACCOUNTS })}
        </p>
      ) : null}

      <button
        type="button"
        onClick={() => store.clear()}
        className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
      >
        {t.clear}
      </button>
    </div>
  );
}
