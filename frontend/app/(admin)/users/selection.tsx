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

/** One picked account, as the selection panel below needs to name it. */
export type SelectedAccount = { id: string; display: string };

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
 *
 * This now lives one level up, in `layout.tsx`, rather than inside the page —
 * that is what lets a pick survive a search, which re-renders `page.tsx` but
 * not the layout above it. That is also why an id is kept with a `display`
 * label rather than bare: once a pick can outlive the page that showed it, an
 * account a later search has pushed off screen still has to be nameable in
 * the "who is selected" panel below, using the label captured when it was
 * checked rather than something only the current page's rows can supply.
 */
class SelectionStore {
  private entries = new Map<string, string>();
  private listeners = new Set<() => void>();
  // Both `selected()` and `list()` hand back a cached snapshot rather than a
  // fresh array on every read, which is what useSyncExternalStore needs: it
  // calls the getSnapshot function on every render to check for change, and a
  // fresh array each time never compares equal to the last one, so React
  // treats every render as a fresh update and can loop. Rebuilding only on an
  // actual mutation keeps the reference stable between emits.
  private cachedSelected: string[] | null = null;
  private cachedList: SelectedAccount[] | null = null;
  // Whose selection this is, as of the last `syncOwner` call. `undefined`
  // until that first call — deliberately distinct from every real value
  // `owner` carries (`string | null`) so a fresh store's first sync always
  // "matches" rather than clearing entries that cannot exist yet.
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

  /** Every picked account, by the label it was given when checked — what the
   * "view selection" panel lists. */
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

  /**
   * Adds every row given, leaving anything already picked — on another page,
   * from an earlier search — exactly as it was. This is what "select this
   * page" has to do now that a pick can span pages: overwriting the whole
   * store here would silently drop everything not currently on screen, which
   * is the header checkbox reaching further than what it shows.
   */
  selectMany = (rows: SelectedAccount[]) => {
    for (const row of rows) this.entries.set(row.id, row.display);
    this.emit();
  };

  /** Removes exactly these ids and no others — "deselect this page", and a
   * single row's own remove button in the selection panel, both go through
   * this rather than through `clear`. */
  deselectMany = (ids: readonly string[]) => {
    for (const id of ids) this.entries.delete(id);
    this.emit();
  };

  /** Drops the whole selection — "Clear selection", and what a bulk run that
   * actually changed something does on its way out. */
  clear = () => {
    this.entries.clear();
    this.emit();
  };

  /**
   * Scopes this store to whoever is signed in, clearing it the moment that
   * changes.
   *
   * The store now lives in a layout precisely so a pick survives a search
   * (see the class doc above) — the same React identity that makes that work
   * is exactly what could let a pick survive further than that. Next keeps a
   * client-side cache of previously rendered route segments to make
   * back/forward navigation instant and avoid layout shift
   * (`node_modules/next/dist/docs/01-app/03-api-reference/05-config/01-next-config-js/staleTimes.md`:
   * "This doesn't change back/forward caching behavior..."), and nothing
   * about that cache is scoped to who is signed in. Rather than establishing
   * whether it can actually resurrect this component's state across a
   * sign-out and a different administrator signing in — in the same tab, a
   * still-open back-navigation reaching a cached copy of this tree — `owner`
   * is checked every time `SelectionProvider` runs, and a change clears the
   * selection outright, regardless of *why* this instance is being asked
   * about a different administrator than the one it last held picks for.
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
 * Holds the one store instance for the accounts on this page.
 *
 * The instance itself travels through context, which is fine — it never
 * changes identity, so nothing that reads it re-renders when the selection
 * does. What must never live in context is the selected set itself.
 *
 * `owner` is the signed-in administrator's id, read fresh, server-side, by
 * `layout.tsx` on every request that reaches it — see `SelectionStore.syncOwner`
 * for why a plain `useState` here is not enough on its own to keep a
 * selection from outliving whoever made it. Optional and defaulted to
 * `null` for the many tests in this file that exercise the selection
 * mechanics and have no administrator identity to give it; `layout.tsx`
 * always passes a real one.
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

/** The ids currently picked, read by the bulk actions below. */
export function useSelectedIds(): readonly string[] {
  const store = useSelectionStore();
  return useSyncExternalStore(store.subscribe, store.selected, () => EMPTY_SELECTION);
}

/**
 * One row's box. Subscribes only to its own membership, so ticking it
 * re-renders this row and nothing else on the page.
 *
 * `display` is what names this account in the "view selection" panel if it
 * is still picked once a later search has taken it off screen — it defaults
 * to `id`, which is enough for a checkbox nothing else reads by name (most
 * tests), but the register itself always passes the account's own login.
 *
 * `false` as the server snapshot: the server never knows about a selection,
 * so the first client render must agree with the server-rendered markup
 * (unchecked) or React reports a hydration mismatch.
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
 * The header box: picks or clears every id on the visible page, and shows
 * the mixed state while only some of them are picked.
 *
 * Goes through `selectMany`/`deselectMany`, never `clear` or a full
 * overwrite: a selection can now hold accounts from other pages, and this
 * box speaks for the current page only — ticking it must add to whatever is
 * already picked elsewhere, and unticking it must remove only what it added,
 * not reach past what it shows. `pickedHere`/`allPicked`/`somePicked` are
 * already scoped to `ids` (this page), which is what keeps the box truthful
 * regardless of how much of the selection lives off it.
 */
export function SelectAllCheckbox({
  ids,
  displays,
  label,
}: {
  ids: string[];
  /** id -> display label, used only when picking. Falls back to the id
   * itself where omitted, which is enough for tests that never open the
   * selection panel; the register always supplies real logins. */
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

/**
 * Names every currently selected account, with a way to drop any one of them.
 *
 * Follows `SkippedList`'s own shape on purpose — the same scrollable list,
 * the same row layout, the same login-or-id naming — because it answers the
 * same kind of question ("which accounts, exactly") that list already
 * answers for a bulk outcome's skipped rows. Once a pick can span several
 * searches, "how many are selected" stops being enough on its own: an
 * account picked, then pushed off screen by a later search, is still fully
 * part of what a destructive bulk action will touch, and this is where it
 * stays visible without being hunted for.
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

/**
 * The trigger and dialog that let the administrator see who, exactly, is
 * selected — not only how many. A plain read of the store, not a form: it
 * holds no pending state of its own, so it stays dismissible throughout,
 * unlike the bulk-action dialogs below.
 */
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
  // `reportDismissible` lets the form inside say whether Escape, an outside
  // click and the corner X may currently close this dialog — the form is the
  // one that knows whether a request is pending or a result is on screen, so
  // it is the one that decides, through this callback, rather than this
  // component guessing from the outside.
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
      {/* The dialog's own content only actually mounts while `open` is true
          — DialogPortal defaults to `keepMounted={false}` — so the form
          inside, and the useActionState it holds, starts fresh every time
          this reopens rather than showing the last run's result. */}
      <Dialog open={open} onOpenChange={setOpen} dismissible={dismissible}>
        <DialogContent closeLabel={closeLabel} dismissible={dismissible}>
          {children(() => setOpen(false), setDismissible)}
        </DialogContent>
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
  // Frozen at mount rather than read live: this form remounts fresh every
  // time the dialog opens (see `ActionDialog`), and a successful run clears
  // the selection on close (below) — the title and the submitted ids must
  // not drift out from under an outcome that is still on screen.
  const [frozenIds] = useState(ids);
  const title = titleTemplate.replace("{n}", String(frozenIds.length));
  const [state, formAction, pending] = useActionState<BulkState, FormData>(action, {});
  const [reasonMissing, setReasonMissing] = useState(false);

  // While a request is in flight, or while its result is on screen, Escape,
  // an outside click and the corner X must not be able to discard it — only
  // the explicit "Done" below can. Before that, dismissal stays open.
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
              // Nothing changed is not cleared, so a mistaken pick can be
              // corrected and retried without reselecting everything by
              // hand; a run that changed at least one account is done with
              // this selection.
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
            onChange={(event) => {
              // Clear the error the moment it is corrected, rather than
              // leaving the red line and `aria-invalid` on screen until the
              // next submit attempt re-evaluates it.
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
 * How long the confirmation screen's own submit button stays disabled once it
 * appears. That screen renders shorter than the one before it — no role
 * checkboxes, just a warning — so the button a first, empty submit was
 * intercepted from sits higher on screen than the "Remove all roles" button
 * that replaces it. A fast double-click aimed at the first button lands its
 * second hit here by pure layout accident, and until this delay, nothing
 * stopped that from going through: the screen looked like a fresh step but
 * behaved like an unconfirmed one. A person confirming on purpose waits this
 * long anyway; a stray click inside it does nothing.
 */
export const CONFIRM_EMPTY_ARM_MS = 400;

/**
 * Replaces the role set on every selected account with what is checked here.
 *
 * Sending an empty set is legitimate — that is how every role is deliberately
 * stripped from an account — but offering it as the dialog's silent default,
 * behind a button that reads as a question, is not: an administrator who
 * opens this to look, then confirms without ticking anything, would strip
 * every role from the whole selection in one click. A submit with nothing
 * checked is intercepted once and answered with a second, explicit step that
 * names the consequence and the count; ticking at least one role still
 * submits in a single step, exactly as before.
 *
 * That second step's own submit is guarded independently of the first: it
 * disarms itself whenever the empty-roles screen is not the one showing, and
 * only arms again after `CONFIRM_EMPTY_ARM_MS` of that screen actually being
 * on-screen (`confirmArmed`), with the emptiness re-checked from the form's
 * own data at the moment of submission rather than trusted from whichever
 * click first raised this screen. Nothing about reaching this step is
 * treated as consent to leave it — that is what makes the guard survive a
 * second click rather than being spent by the first.
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
  // Set once an empty submit has been intercepted, to show the explicit
  // "remove all roles" confirmation in place of the role picker.
  const [confirmingEmpty, setConfirmingEmpty] = useState(false);
  // Whether that confirmation's own submit button may actually be pressed —
  // see `CONFIRM_EMPTY_ARM_MS` above. False the instant the confirmation
  // stops showing, so leaving and reopening it never inherits an earlier arm.
  const [confirmArmed, setConfirmArmed] = useState(false);

  useEffect(() => {
    reportDismissible(!pending && !state.result);
  }, [pending, state.result, reportDismissible]);

  // Arms the confirmation's submit button after it has genuinely been
  // showing for `CONFIRM_EMPTY_ARM_MS`. The two places that flip
  // `confirmingEmpty` — the interception below and "Go back" — reset
  // `confirmArmed` to `false` themselves, in the same event, rather than this
  // effect doing it on the way out: a `setState` call synchronous in an
  // effect body is its own cascading-render footgun, and there is nothing
  // here that needs the DOM to have committed first.
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
          // Re-checked here, at the moment of submission, rather than
          // trusted from the click that raised this screen: this view
          // renders no role checkboxes, so the set is empty by
          // construction, but the guard belongs at the point of submit
          // regardless of why it currently holds. `confirmArmed` is what
          // actually stops a stray click — a second hit landing on this
          // button before it has been showing long enough to be a
          // deliberate press, most often the tail of a fast double-click
          // whose first half only got this far because the screen it
          // opened is shorter than the one it replaced.
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
              // A later re-entry into this screen must start unarmed again,
              // not inherit an arming this earlier visit already earned.
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
          // Nothing ticked: this is the dialog's silent default, and
          // confirming it as-is would strip every role from the whole
          // selection. Ask once, in plain words, before letting it through.
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
 * Says how many accounts are picked and offers what can be done to all of
 * them at once: block, unblock, delete, change roles, reset passwords.
 *
 * `roles` is the catalogue the server publishes (already fetched beside the
 * register), so the roles dialog offers exactly what the single-account card
 * does and nothing this build has to keep in step by hand.
 */
export function SelectionBar({
  dict,
  roles,
  pageIds,
}: {
  dict: Dictionary;
  roles: Role[];
  /** The ids on the page currently shown — used only to say how much of the
   * whole selection is not in view; never to limit what a bulk action below
   * touches, which always acts on the entire selection. */
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
  // The register never offers more than MAX_BULK_ACCOUNTS in one pick today
  // (a page is 50 rows), but the bar still refuses to send more than the
  // backend will accept rather than relying on that staying true.
  const tooMany = count > MAX_BULK_ACCOUNTS;
  // How much of the selection this page cannot show — the honesty a
  // selection that outlives a search now owes: an administrator who picked
  // twelve accounts, searched for something else, and can currently see
  // three of them needs to be told the other nine are still part of what
  // "Delete" below would touch.
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
