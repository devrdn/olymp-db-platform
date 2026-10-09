"use client";

import {
  createContext,
  useActionState,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
} from "react";

import { X } from "lucide-react";

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
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  bulkBlockAction,
  bulkDeleteAction,
  bulkReplaceRolesAction,
  bulkResetPasswordAction,
  bulkUnblockAction,
  type BulkPasswordState,
  type BulkState,
} from "./bulk-actions";
import { messageForCode } from "@/lib/i18n/errors";

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

function HiddenIds({ ids }: { ids: readonly string[] }) {
  return (
    <>
      {ids.map((id) => (
        <input key={id} type="hidden" name="ids" value={id} />
      ))}
    </>
  );
}

/**
 * Accounts a bulk operation declined, by login with the reason. Never hidden,
 * since each was selected deliberately. An unknown reason from a newer server
 * is shown raw.
 */
function SkippedList({ skipped, dict }: { skipped: readonly SkippedAccount[]; dict: Dictionary }) {
  if (skipped.length === 0) return null;
  const reasons = dict.accounts.selection.bulk.reason as Record<string, string>;

  return (
    <ul className="flex max-h-48 flex-col gap-1 overflow-y-auto border-t border-line pt-3">
      {skipped.map((row) => (
        <li key={row.id} className="font-mono text-data text-ink-2">
          {/* A not_found row carries no login. */}
          {row.login || row.id}
          <span className="ml-3 text-warn">{reasons[row.reason] ?? row.reason}</span>
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
            <span className="sr-only">{t.unpick.replace("{name}", row.display)}</span>
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
        <span className="text-good">{t.changed.replace("{n}", String(changedCount))}</span>
        {skipped.length > 0 ? (
          <span className="text-warn"> · {t.skipped.replace("{n}", String(skipped.length))}</span>
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
  const title = titleTemplate.replace("{n}", String(frozenIds.length));
  const [state, formAction, pending] = useActionState<BulkState, FormData>(action, {});
  const [reasonMissing, setReasonMissing] = useState(false);

  // While pending or showing a result, only "Done" may close the dialog.
  useEffect(() => {
    reportDismissible(!pending && !state.result);
  }, [pending, state.result, reportDismissible]);

  if (state.result) {
    const changedCount = state.result.changed.length;

    return (
      <>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <ChangedSkipped changedCount={changedCount} skipped={state.result.skipped} dict={dict} />
        <DialogFooter>
          <Button
            type="button"
            onClick={() => {
              // Kept when nothing changed, so a mistaken pick can be fixed and
              // retried.
              if (changedCount > 0) store.clear();
              onClose();
            }}
          >
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
    <form
      action={formAction}
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
      <HiddenIds ids={frozenIds} />

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

      {reasonMissing ? (
        <p role="alert" className="text-small text-bad">
          {dict.errors.reason_required}
        </p>
      ) : failure ? (
        <p role="alert" className="text-small text-bad">
          {failure}
        </p>
      ) : null}

      <DialogFooter>
        <Button type="button" variant="quiet" onClick={onClose} disabled={pending}>
          {t.cancel}
        </Button>
        <Button type="submit" variant={danger ? "danger" : "primary"} disabled={pending}>
          {pending ? t.submitting : t.confirm}
        </Button>
      </DialogFooter>
    </form>
  );
}

/**
 * How long the "remove all roles" button stays disabled after appearing. The
 * confirmation is shorter than the form, so the second click of a fast
 * double-click can land on its button.
 */
export const CONFIRM_EMPTY_ARM_MS = 400;

/**
 * Replaces the role set on every selected account. An empty set is legitimate
 * but must not be the silent default: an empty submit is intercepted and
 * replaced by an explicit confirmation naming the count.
 *
 * That confirmation's submit is armed only after `CONFIRM_EMPTY_ARM_MS` on
 * screen, disarmed whenever it leaves, and re-checks emptiness from the form
 * data at submit.
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
  const title = t.rolesDialog.title.replace("{n}", String(frozenIds.length));
  const [state, formAction, pending] = useActionState<BulkState, FormData>(
    bulkReplaceRolesAction,
    {},
  );
  // Set once an empty submit is intercepted.
  const [confirmingEmpty, setConfirmingEmpty] = useState(false);
  // False the instant the confirmation stops showing, so reopening never
  // inherits an earlier arm.
  const [confirmArmed, setConfirmArmed] = useState(false);

  useEffect(() => {
    reportDismissible(!pending && !state.result);
  }, [pending, state.result, reportDismissible]);

  // The handlers that flip `confirmingEmpty` reset `confirmArmed` themselves; a
  // synchronous `setState` in an effect would cascade renders.
  useEffect(() => {
    if (!confirmingEmpty) return;
    const timer = setTimeout(() => setConfirmArmed(true), CONFIRM_EMPTY_ARM_MS);
    return () => clearTimeout(timer);
  }, [confirmingEmpty]);

  if (state.result) {
    const changedCount = state.result.changed.length;

    return (
      <>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <ChangedSkipped changedCount={changedCount} skipped={state.result.skipped} dict={dict} />
        <DialogFooter>
          <Button
            type="button"
            onClick={() => {
              if (changedCount > 0) store.clear();
              onClose();
            }}
          >
            {t.done}
          </Button>
        </DialogFooter>
      </>
    );
  }

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
    : null;

  if (confirmingEmpty) {
    return (
      <form
        action={formAction}
        onSubmit={(event) => {
          // Re-checked at submit rather than trusted from the click that raised
          // this screen; `confirmArmed` stops a stray double-click.
          const checked = new FormData(event.currentTarget).getAll("roles");
          if (checked.length > 0 || !confirmArmed) {
            event.preventDefault();
          }
        }}
        className="flex flex-col gap-5"
      >
        <HiddenIds ids={frozenIds} />
        <DialogHeader>
          <DialogTitle>{t.rolesDialog.confirmEmptyTitle}</DialogTitle>
          <DialogDescription>
            {t.rolesDialog.confirmEmptyBody.replace("{n}", String(frozenIds.length))}
          </DialogDescription>
        </DialogHeader>

        {failure ? (
          <p role="alert" className="text-small text-bad">
            {failure}
          </p>
        ) : null}

        <DialogFooter>
          <Button
            type="button"
            variant="quiet"
            onClick={() => {
              setConfirmingEmpty(false);
              // A later visit must start unarmed.
              setConfirmArmed(false);
            }}
            disabled={pending}
          >
            {t.rolesDialog.back}
          </Button>
          <Button type="submit" variant="danger" disabled={pending || !confirmArmed}>
            {pending ? t.submitting : t.rolesDialog.confirmEmptySubmit}
          </Button>
        </DialogFooter>
      </form>
    );
  }

  return (
    <form
      action={formAction}
      onSubmit={(event) => {
        const checked = new FormData(event.currentTarget).getAll("roles");
        if (checked.length === 0) {
          // Nothing ticked: ask before stripping every role from the selection.
          event.preventDefault();
          setConfirmingEmpty(true);
        }
      }}
      className="flex flex-col gap-5"
    >
      <HiddenIds ids={frozenIds} />

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
          {pending ? t.submitting : t.rolesDialog.submit}
        </Button>
      </DialogFooter>
    </form>
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
  onClose,
  reportDismissible,
}: {
  ids: readonly string[];
  dict: Dictionary;
  onClose: () => void;
  reportDismissible: (value: boolean) => void;
}) {
  const t = dict.accounts.selection.bulk;
  const store = useSelectionStore();
  const [frozenIds] = useState(ids);
  const [state, formAction, pending] = useActionState<BulkPasswordState, FormData>(
    bulkResetPasswordAction,
    {},
  );

  useEffect(() => {
    reportDismissible(!pending && !state.result);
  }, [pending, state.result, reportDismissible]);

  if (state.result) {
    const issuedCount = state.result.issued.length;
    const skippedCount = state.result.skipped.length;

    return (
      <>
        <DialogHeader>
          <DialogTitle>{t.resetDialog.issued}</DialogTitle>
          <DialogDescription>{t.resetDialog.handover}</DialogDescription>
        </DialogHeader>

        {issuedCount > 0 ? (
          <ul className="flex max-h-64 flex-col gap-2 overflow-y-auto">
            {state.result.issued.map((row) => (
              <IssuedRow key={row.id} row={row} dict={dict} />
            ))}
          </ul>
        ) : null}

        <SkippedList skipped={state.result.skipped} dict={dict} />

        {issuedCount === 0 && skippedCount === 0 ? (
          <p className="text-small text-ink-2">{t.resetDialog.none}</p>
        ) : null}

        <DialogFooter>
          <Button
            type="button"
            onClick={() => {
              if (issuedCount > 0) store.clear();
              onClose();
            }}
          >
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
      <HiddenIds ids={frozenIds} />

      <DialogHeader>
        <DialogTitle>{t.resetDialog.title.replace("{n}", String(frozenIds.length))}</DialogTitle>
        <DialogDescription>{t.resetDialog.description}</DialogDescription>
      </DialogHeader>

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
          {pending ? t.submitting : t.resetDialog.submit}
        </Button>
      </DialogFooter>
    </form>
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
}: {
  dict: Dictionary;
  roles: Role[];
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
        {t.count.replace("{n}", String(count))}
        {offPage > 0 ? (
          <span className="text-warn"> · {t.offPage.replace("{n}", String(offPage))}</span>
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
              onClose={close}
              reportDismissible={reportDismissible}
            />
          )}
        </ActionDialog>
      </div>

      {tooMany ? (
        <p role="alert" className="w-full text-small text-bad">
          {bulk.tooMany.replace("{n}", String(MAX_BULK_ACCOUNTS))}
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
