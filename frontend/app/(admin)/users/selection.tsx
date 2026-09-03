"use client";

import { createContext, useActionState, useContext, useState, useSyncExternalStore } from "react";

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

/**
 * Which accounts the administrator has picked.
 *
 * An external store read through useSyncExternalStore rather than a context
 * value, because a context re-renders every consumer on every change: with
 * fifty rows on a page, ticking one box would re-render fifty checkboxes and
 * the bar. Here each checkbox subscribes to its own membership and the bar to
 * the count, so a click re-renders one row.
 *
 * The page itself stays a server component. Only the boxes, the bar and the
 * dialogs below it are client code, which is the whole of what needs state.
 */
class SelectionStore {
  private ids = new Set<string>();
  private listeners = new Set<() => void>();
  // `selected()` used to hand back a snapshot every read is what
  // useSyncExternalStore needs: it calls the getSnapshot function on every
  // render to check for change, and a fresh array each time never compares
  // equal to the last one, so React treats every render as a fresh update
  // and can loop. Caching the array and only rebuilding it on an actual
  // mutation keeps the reference stable between emits.
  private cachedSelected: string[] | null = null;

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  has = (id: string) => this.ids.has(id);
  size = () => this.ids.size;
  selected = () => {
    if (this.cachedSelected === null) this.cachedSelected = [...this.ids];
    return this.cachedSelected;
  };

  toggle = (id: string) => {
    if (!this.ids.delete(id)) this.ids.add(id);
    this.emit();
  };

  replace = (ids: string[]) => {
    this.ids = new Set(ids);
    this.emit();
  };

  private emit() {
    this.cachedSelected = null;
    for (const listener of this.listeners) listener();
  }
}

const EMPTY_SELECTION: readonly string[] = [];

const SelectionContext = createContext<SelectionStore | null>(null);

function useSelectionStore(): SelectionStore {
  const store = useContext(SelectionContext);
  if (!store) {
    throw new Error("useSelectionStore must be used within a SelectionProvider");
  }
  return store;
}

/**
 * Holds the one store instance for the accounts on this page.
 *
 * The instance itself travels through context, which is fine — it never
 * changes identity, so nothing that reads it re-renders when the selection
 * does. What must never live in context is the selected set itself.
 */
export function SelectionProvider({ children }: { children: React.ReactNode }) {
  const [store] = useState(() => new SelectionStore());
  return <SelectionContext.Provider value={store}>{children}</SelectionContext.Provider>;
}

/** The ids currently picked, read by the bulk actions below. */
export function useSelectedIds(): readonly string[] {
  const store = useSelectionStore();
  return useSyncExternalStore(store.subscribe, store.selected, () => EMPTY_SELECTION);
}

/**
 * One row's box. Subscribes only to its own membership, so ticking it
 * re-renders this row and nothing else on the page.
 *
 * `false` as the server snapshot: the server never knows about a selection,
 * so the first client render must agree with the server-rendered markup
 * (unchecked) or React reports a hydration mismatch.
 */
export function RowCheckbox({ id, label }: { id: string; label: string }) {
  const store = useSelectionStore();
  const checked = useSyncExternalStore(store.subscribe, () => store.has(id), () => false);

  return <Checkbox checked={checked} onCheckedChange={() => store.toggle(id)} aria-label={label} />;
}

/**
 * The header box: picks or clears every id on the visible page, and shows
 * the mixed state while only some of them are picked.
 */
export function SelectAllCheckbox({ ids, label }: { ids: string[]; label: string }) {
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
      onCheckedChange={() => store.replace(allPicked ? [] : ids)}
      aria-label={label}
    />
  );
}

/** Every selected id, as the hidden fields a bulk form sends. */
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
 * Every account a bulk operation declined to touch, named by login with its
 * reason in the interface's own words.
 *
 * An account the operation declined to touch is not an error and must never
 * be hidden — the administrator selected it deliberately. `reason` is looked
 * up in the closed vocabulary this build translates (`accounts.selection.bulk.reason`,
 * mirroring `SKIP_REASONS` in `lib/api/accounts-terms.ts`) and shown raw when
 * the lookup misses: a reason a newer server has shipped is still an outcome
 * the administrator has to see, under whatever name it arrived with. The same
 * shape the roster import panel uses for its own skipped rows
 * (`people-panels.tsx`'s `ImportParticipants`).
 */
function SkippedList({ skipped, dict }: { skipped: readonly SkippedAccount[]; dict: Dictionary }) {
  if (skipped.length === 0) return null;
  const reasons = dict.accounts.selection.bulk.reason as Record<string, string>;

  return (
    <ul className="flex max-h-48 flex-col gap-1 overflow-y-auto border-t border-line pt-3">
      {skipped.map((row) => (
        <li key={row.id} className="font-mono text-data text-ink-2">
          {/* A not_found row carries no login — the id is what is left to
              name it by. */}
          {row.login || row.id}
          <span className="ml-3 text-warn">{reasons[row.reason] ?? row.reason}</span>
        </li>
      ))}
    </ul>
  );
}

/** The "N changed / M skipped" line every bulk outcome shares. */
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

/** One password issued in a bulk reset, with a way to copy it. */
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
            // Clipboard access can be refused (insecure origin, no
            // permission). The password stays on screen and selectable by
            // hand either way — see `select-all` above.
          }
        }}
      >
        {copied ? t.copied : t.copy}
      </Button>
    </li>
  );
}

/** A trigger button and the dialog it opens, closed by default. */
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
  children: (close: () => void) => React.ReactNode;
}) {
  const [open, setOpen] = useState(false);

  return (
    <>
      <Button
        type="button"
        variant={triggerVariant}
        size="sm"
        disabled={disabled}
        onClick={() => setOpen(true)}
      >
        {triggerLabel}
      </Button>
      {/* The dialog's own content only actually mounts while `open` is true
          — DialogPortal defaults to `keepMounted={false}` — so the form
          inside, and the useActionState it holds, starts fresh every time
          this reopens rather than showing the last run's result. */}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={closeLabel}>{children(() => setOpen(false))}</DialogContent>
      </Dialog>
    </>
  );
}

/**
 * The form behind block, unblock and delete: all three are the same status
 * change, and only block and delete are asked for a reason. The server
 * refuses an empty one for those two either way (`ErrReasonRequired`); this
 * is what stops the request from leaving in the first place.
 */
function StatusForm({
  ids,
  dict,
  title,
  description,
  requireReason,
  danger,
  action,
  onClose,
}: {
  ids: readonly string[];
  dict: Dictionary;
  title: string;
  description: string;
  requireReason: boolean;
  danger: boolean;
  action: (previous: BulkState, form: FormData) => Promise<BulkState>;
  onClose: () => void;
}) {
  const t = dict.accounts.selection.bulk;
  const [state, formAction, pending] = useActionState<BulkState, FormData>(action, {});
  const [reasonMissing, setReasonMissing] = useState(false);

  if (state.result) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <ChangedSkipped
          changedCount={state.result.changed.length}
          skipped={state.result.skipped}
          dict={dict}
        />
        <DialogFooter>
          <Button type="button" onClick={onClose}>
            {t.done}
          </Button>
        </DialogFooter>
      </>
    );
  }

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  return (
    <form
      action={formAction}
      onSubmit={(event) => {
        if (!requireReason) return;
        const reason = String(new FormData(event.currentTarget).get("reason") ?? "").trim();
        if (reason === "") {
          // Caught here, before the request ever leaves: an empty or
          // whitespace-only reason must never reach the server, which is
          // the one thing this handler exists to guarantee.
          event.preventDefault();
          setReasonMissing(true);
        } else {
          setReasonMissing(false);
        }
      }}
      className="flex flex-col gap-5"
      noValidate
    >
      <HiddenIds ids={ids} />

      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </DialogHeader>

      {requireReason ? (
        <div className="flex flex-col gap-2">
          <label htmlFor="bulk-reason" className="font-mono text-label text-ink-3 uppercase">
            {t.reasonLabel}
          </label>
          {/* `required` stays for its ARIA semantics; the form carries
              `noValidate` so the browser's own — unstyled, unlocalised —
              validation bubble never fires, and the check above is what
              actually decides whether the request leaves. */}
          <Textarea
            id="bulk-reason"
            name="reason"
            required
            aria-invalid={reasonMissing || undefined}
            className="min-h-24 font-sans text-body"
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

/** Replaces the role set on every selected account with what is checked here. */
function RolesForm({
  ids,
  roles,
  dict,
  onClose,
}: {
  ids: readonly string[];
  roles: Role[];
  dict: Dictionary;
  onClose: () => void;
}) {
  const t = dict.accounts.selection.bulk;
  const [state, formAction, pending] = useActionState<BulkState, FormData>(
    bulkReplaceRolesAction,
    {},
  );
  const title = t.rolesDialog.title.replace("{n}", String(ids.length));

  if (state.result) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <ChangedSkipped
          changedCount={state.result.changed.length}
          skipped={state.result.skipped}
          dict={dict}
        />
        <DialogFooter>
          <Button type="button" onClick={onClose}>
            {t.done}
          </Button>
        </DialogFooter>
      </>
    );
  }

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  return (
    <form action={formAction} className="flex flex-col gap-5">
      <HiddenIds ids={ids} />

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
 * Issues a fresh one-time password for every selected account.
 *
 * A confirmation step first — this is the one bulk action with no reason to
 * type and an effect just as real, ending every open session of every
 * account it touches — and then the passwords themselves, each shown until
 * the administrator is done with it and never retrievable again.
 */
function ResetPasswordForm({
  ids,
  dict,
  onClose,
}: {
  ids: readonly string[];
  dict: Dictionary;
  onClose: () => void;
}) {
  const t = dict.accounts.selection.bulk;
  const [state, formAction, pending] = useActionState<BulkPasswordState, FormData>(
    bulkResetPasswordAction,
    {},
  );

  if (state.result) {
    return (
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

        <SkippedList skipped={state.result.skipped} dict={dict} />

        <DialogFooter>
          <Button type="button" onClick={onClose}>
            {t.done}
          </Button>
        </DialogFooter>
      </>
    );
  }

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  return (
    <form action={formAction} className="flex flex-col gap-5">
      <HiddenIds ids={ids} />

      <DialogHeader>
        <DialogTitle>{t.resetDialog.title.replace("{n}", String(ids.length))}</DialogTitle>
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
 * Says how many accounts are picked and offers what can be done to all of
 * them at once: block, unblock, delete, change roles, reset passwords.
 *
 * `roles` is the catalogue the server publishes (already fetched beside the
 * register), so the roles dialog offers exactly what the single-account card
 * does and nothing this build has to keep in step by hand.
 */
export function SelectionBar({ dict, roles }: { dict: Dictionary; roles: Role[] }) {
  const store = useSelectionStore();
  const count = useSyncExternalStore(store.subscribe, () => store.size(), () => 0);
  const ids = useSelectedIds();
  const t = dict.accounts.selection;
  const bulk = t.bulk;
  // The register never offers more than MAX_BULK_ACCOUNTS in one pick today
  // (a page is 50 rows), but the bar still refuses to send more than the
  // backend will accept rather than relying on that staying true.
  const tooMany = count > MAX_BULK_ACCOUNTS;

  if (count === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-3 border border-line-2 bg-panel px-4 py-2.5">
      <p role="status" aria-live="polite" className="font-mono text-data text-ink-2">
        {t.count.replace("{n}", String(count))}
      </p>

      <div data-slot="selection-actions" className="flex flex-1 flex-wrap items-center gap-2">
        <ActionDialog
          triggerLabel={bulk.block}
          triggerVariant="danger"
          disabled={tooMany}
          closeLabel={bulk.cancel}
        >
          {(close) => (
            <StatusForm
              ids={ids}
              dict={dict}
              title={bulk.blockDialog.title.replace("{n}", String(ids.length))}
              description={bulk.blockDialog.description}
              requireReason
              danger
              action={bulkBlockAction}
              onClose={close}
            />
          )}
        </ActionDialog>

        <ActionDialog
          triggerLabel={bulk.unblock}
          triggerVariant="secondary"
          disabled={tooMany}
          closeLabel={bulk.cancel}
        >
          {(close) => (
            <StatusForm
              ids={ids}
              dict={dict}
              title={bulk.unblockDialog.title.replace("{n}", String(ids.length))}
              description={bulk.unblockDialog.description}
              requireReason={false}
              danger={false}
              action={bulkUnblockAction}
              onClose={close}
            />
          )}
        </ActionDialog>

        <ActionDialog
          triggerLabel={bulk.delete}
          triggerVariant="danger"
          disabled={tooMany}
          closeLabel={bulk.cancel}
        >
          {(close) => (
            <StatusForm
              ids={ids}
              dict={dict}
              title={bulk.deleteDialog.title.replace("{n}", String(ids.length))}
              description={bulk.deleteDialog.description}
              requireReason
              danger
              action={bulkDeleteAction}
              onClose={close}
            />
          )}
        </ActionDialog>

        <ActionDialog
          triggerLabel={bulk.roles}
          triggerVariant="secondary"
          disabled={tooMany}
          closeLabel={bulk.cancel}
        >
          {(close) => <RolesForm ids={ids} roles={roles} dict={dict} onClose={close} />}
        </ActionDialog>

        <ActionDialog
          triggerLabel={bulk.resetPassword}
          triggerVariant="secondary"
          disabled={tooMany}
          closeLabel={bulk.cancel}
        >
          {(close) => <ResetPasswordForm ids={ids} dict={dict} onClose={close} />}
        </ActionDialog>
      </div>

      {tooMany ? (
        <p role="alert" className="w-full text-small text-bad">
          {bulk.tooMany.replace("{n}", String(MAX_BULK_ACCOUNTS))}
        </p>
      ) : null}

      <button
        type="button"
        onClick={() => store.replace([])}
        className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
      >
        {t.clear}
      </button>
    </div>
  );
}
