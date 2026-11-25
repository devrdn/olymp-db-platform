"use client";

import {
  createContext,
  Fragment,
  startTransition,
  useActionState,
  useContext,
  useEffect,
  useRef,
  useState,
  type ComponentProps,
  type FormEvent,
  type ReactNode,
} from "react";

import { ReloadButton } from "@/components/product/reload-button";
import { Button } from "@/components/ui/button";
import { type Question, useConfirm } from "@/components/ui/use-confirm";
import type { ActionState } from "@/lib/api/server";
import { messageForCode, type ErrorSentences } from "@/lib/i18n/errors";
import { labelFor } from "@/lib/i18n/label";
import { useHydrated } from "@/lib/use-hydrated";
import { cn } from "@/lib/utils";

/**
 * The question a destructive form asks before it sends, or a function of what
 * is about to be sent that returns the question, or null when this submission
 * needs none.
 */
export type Confirmation = Question | ((form: FormData) => Question | null);

type Bound = {
  state: ActionState;
  pending: boolean;
  /** The submit cannot be pressed yet: the form asks first and is not hydrated. */
  locked: boolean;
  /** A code the form refused itself before sending: its question could not be asked. */
  unasked?: string;
  errors: ErrorSentences;
  /** Mounts the form afresh: its fields, its state and how the last submission went. */
  restart: () => void;
  /** Marks the draft edited, which keeps the version it is based on (`version`). */
  edited: () => void;
};

const BoundForm = createContext<Bound | null>(null);

function useBound(component: string): Bound {
  const bound = useContext(BoundForm);
  if (!bound) throw new Error(`${component} must sit inside an ActionForm`);
  return bound;
}

/**
 * Whether the enclosing ActionForm's action is running, and how its last
 * submission went (it worked, or the code it was refused with), for a part
 * that reacts to either: a dialog that closes on success or may not be
 * dismissed while pending, an offer only one refusal makes.
 */
export function useActionStatus(): { pending: boolean; ok: boolean; code?: string } {
  const { state, pending } = useBound("useActionStatus");
  return { pending, ok: state.ok === true, code: state.code };
}

/**
 * Marks the enclosing ActionForm's draft edited, for a change no field's input
 * event reports: a row added or removed by a button, a draft held in state.
 */
export function useDraftEdited(): () => void {
  return useBound("useDraftEdited").edited;
}

/**
 * Tells a dialog holding the form whether it may be dismissed: not while the
 * action runs, nor while `holding` (a result that must be acknowledged).
 * `onSuccess` runs once a submission worked.
 */
export function ActionDialogBinding({
  reportDismissible,
  holding = false,
  onSuccess,
}: {
  reportDismissible: (value: boolean) => void;
  holding?: boolean;
  onSuccess?: () => void;
}) {
  const { pending, ok } = useActionStatus();

  useEffect(() => {
    reportDismissible(!pending && !holding);
  }, [pending, holding, reportDismissible]);

  useEffect(() => {
    if (ok) onSuccess?.();
  }, [ok, onSuccess]);

  return null;
}

/** What a submission sends, comparable: the data a yes was given for. */
function sent(form: FormData): string {
  return JSON.stringify(
    [...form.entries()].map(([name, value]) =>
      typeof value === "string" ? [name, value] : [name, value.name, value.size, value.lastModified],
    ),
  );
}

const EMPTY = {};

/**
 * An edited draft's version, the server copy it shows and the answer it was
 * edited under; `inFlight` for an edit made while its own save was sending.
 */
type Pin = { version: number; revision?: string | number; state: ActionState; inFlight?: boolean };

type ActionFormProps<S extends ActionState> = {
  action: (previous: Awaited<S>, form: FormData) => Promise<S>;
  /** The state before anything is sent; empty unless the action's state needs more. */
  initial?: Awaited<S>;
  fields?: Record<string, string | number | readonly string[]>;
  /**
   * The version of the server's copy the draft is based on, sent as the hidden
   * field `expectedVersion` (`atVersion`). While the form is untouched it is
   * each render's; from the first edit (an input or change event in the form,
   * or `useDraftEdited`) it is the one of that moment, until a save that worked
   * or a restart. So a re-read of the page under an edited draft, after a save
   * of another form or an action the server does not guard, cannot hand the
   * draft a version it was not based on: its save is refused as changed rather
   * than written over a colleague's. An edited draft keeps its `revision` too
   * (below), so a newer server copy never replaces what was typed.
   */
  version?: number;
  confirm?: Confirmation;
  errors: ErrorSentences;
  /**
   * The version of the server's copy the fields show. A new one remounts the
   * form's contents, so uncontrolled fields take the saved values, while how
   * the save went stays on screen; a `key` on the form would forget both. A
   * form with a `version` whose draft is edited keeps the copy it shows until
   * its own save works or a reload starts it over: what was typed is never
   * replaced unannounced, and the save is refused as changed instead.
   */
  revision?: string | number;
  /** Runs before the question; preventing the event sends nothing and asks nothing. */
  onSubmit?: (event: FormEvent<HTMLFormElement>) => void;
  className?: string;
  noValidate?: boolean;
  children: ReactNode | ((state: Awaited<S>, pending: boolean) => ReactNode);
};

/**
 * A form bound to a server action: the action's state, its hidden `fields`,
 * and, given `confirm`, the destructive form that asks first. Its parts
 * (`ActionButton`, `ActionOutcome`, `ActionFooter`, `ActionReload`) read the
 * binding, so a screen lays them out and never handles pending, hydration or
 * a refusal's code itself. `children` may be a function of the state, for a
 * screen that shows what the action returned.
 */
export function ActionForm<S extends ActionState>(props: ActionFormProps<S>) {
  const [restarts, setRestarts] = useState(0);
  return <BoundActionForm key={restarts} {...props} restart={() => setRestarts((count) => count + 1)} />;
}

function BoundActionForm<S extends ActionState>({
  action,
  initial,
  fields = {},
  version,
  confirm,
  errors,
  revision,
  onSubmit,
  className,
  noValidate,
  children,
  restart,
}: ActionFormProps<S> & { restart: () => void }) {
  // Handed over as it is: a server action carries what the server renderer
  // needs to make the form a POST that works without JavaScript.
  const [state, formAction, pending] = useActionState<S, FormData>(action, initial ?? (EMPTY as Awaited<S>));
  const formRef = useRef<HTMLFormElement>(null);
  const hydrated = useHydrated();
  // A submission made before hydration is the form's own POST to the server
  // action and never runs `onSubmit`, where the question is asked, so a form
  // that asks cannot be sent until then.
  const locked = confirm !== undefined && !hydrated;
  const [ask, dialog] = useConfirm(confirm !== undefined);
  // What a yes was given for, set only while its own submission is dispatched.
  const approved = useRef<string | null>(null);
  const [unasked, setUnasked] = useState<string | undefined>(undefined);
  // The version and server copy the draft was based on when it was first
  // edited. A save that worked since stored the draft, so the pin describes
  // nothing any more and the form follows `version` and `revision` again;
  // an edit made while that save was sending is not in it, so its pin holds
  // through that save's answer and is released by the next one. Such a
  // draft is based on what that save stored, so a draft typed while the save
  // was sending saves next without a refusal: an answer that worked names
  // it. With no version in the answer the pin keeps the one the save was
  // sent at, which refuses once that save has stored; after a refused save
  // it is still the current one.
  const [pin, setPin] = useState<Pin | undefined>(undefined);
  const holds = (pinned: Pin | undefined): pinned is Pin =>
    pinned !== undefined && (pinned.state === state || !state.ok || pinned.inFlight === true);
  const versionOf = (pinned: Pin): number =>
    (pinned.inFlight && pinned.state !== state && state.ok ? state.version : undefined) ?? pinned.version;
  const basedOn = holds(pin) ? versionOf(pin) : version;
  const shown = holds(pin) ? pin.revision : revision;
  const edited = () => {
    if (basedOn === undefined) return;
    if (pending) setPin({ version: basedOn, revision: shown, state, inFlight: true });
    else setPin((pinned) => (holds(pinned) ? pinned : { version: basedOn, revision: shown, state }));
  };

  // React resets a form's uncontrolled fields once its `action` settles, a
  // refusal included, which erases what was typed. A hydrated form therefore
  // sends through a transition of its own, and once a send settles only a
  // save that worked starts its fields over. A refusal clears the password
  // fields alone: a password left on screen is for the next person at the
  // machine. Before hydration the form posts through `action` as React does.
  const sending = useRef(false);
  useEffect(() => {
    if (pending) {
      sending.current = true;
      return;
    }
    const form = formRef.current;
    if (!sending.current || !form) return;
    sending.current = false;
    // An edit made during the save is not in what the save stored.
    if (state.ok && !pin?.inFlight) form.reset();
    else if (state.code !== undefined) {
      for (const input of form.querySelectorAll<HTMLInputElement>('input[type="password"]')) input.value = "";
    }
  }, [state, pending, pin]);

  return (
    <BoundForm.Provider value={{ state, pending, locked, unasked, errors, restart, edited }}>
      <form
        ref={formRef}
        action={formAction}
        className={className}
        noValidate={noValidate}
        onInput={edited}
        onChange={edited}
        onSubmit={(event) => {
          // Read and spent before anything can stop this submission, so a yes
          // never carries over to a later one.
          const yesFor = approved.current;
          approved.current = null;
          setUnasked(undefined);
          // From here the in-flight pin is an ordinary one, whether this
          // submission goes on or is stopped, released by the next answer
          // that works.
          setPin((pinned) => (pinned?.inFlight ? { ...pinned, version: versionOf(pinned), state, inFlight: false } : pinned));
          onSubmit?.(event);
          if (event.defaultPrevented) return;

          event.preventDefault();
          const form = event.currentTarget;
          const submitter = (event.nativeEvent as SubmitEvent).submitter;
          const data = new FormData(form, submitter);
          // A yes covers the data it was asked about; anything else asks again.
          const question =
            confirm === undefined || (yesFor !== null && yesFor === sent(data))
              ? null
              : typeof confirm === "function"
                ? confirm(data)
                : confirm;
          if (question === null) {
            startTransition(() => formAction(data));
            return;
          }

          const asked = sent(data);
          ask(question).then(
            (yes) => {
              // The button that was pressed went away while the question was open.
              if (!yes || (submitter !== null && !form.contains(submitter))) return;
              approved.current = asked;
              // Dispatched at once, or not at all when the browser's validation
              // stops it; either way the approval ends here.
              try {
                form.requestSubmit(submitter);
              } finally {
                approved.current = null;
              }
            },
            // The question could not even be shown, so nothing is sent.
            () => setUnasked("unreachable"),
          );
        }}
      >
        {Object.entries(fields).flatMap(([name, value]) =>
          (Array.isArray(value) ? value : [String(value)]).map((one, index) => (
            <input key={`${name}-${index}`} type="hidden" name={name} value={one} />
          )),
        )}
        {version === undefined ? null : <input type="hidden" name="expectedVersion" value={basedOn} />}
        <Fragment key={shown}>{typeof children === "function" ? children(state, pending) : children}</Fragment>
      </form>
      {dialog}
    </BoundForm.Provider>
  );
}

/**
 * The form's submit button: disabled while the action runs and, in a form
 * that asks first, until the page is hydrated. `disabled` adds a reason of the
 * screen's own (a frozen contest, a script over its size).
 */
export function ActionButton({
  disabled,
  pendingLabel,
  children,
  ...props
}: Omit<ComponentProps<typeof Button>, "type"> & { pendingLabel?: ReactNode }) {
  const { pending, locked } = useBound("ActionButton");

  return (
    <Button type="submit" {...props} disabled={disabled || pending || locked}>
      {pending && pendingLabel !== undefined ? pendingLabel : children}
    </Button>
  );
}

/**
 * How the last submission went: the refusal's sentence, or `saved` once it
 * worked. Only the dictionary's sentence is ever shown, so a code never
 * reaches the screen. `refusal` is a code the screen refused itself, before
 * anything was sent; `elsewhere` are codes the screen shows by the field they
 * are about. `narrow` keeps a sentence in a table cell from widening its
 * column.
 */
export function ActionOutcome({
  saved,
  refusal,
  elsewhere = [],
  narrow = false,
  id,
  children,
}: {
  saved?: string;
  refusal?: string;
  elsewhere?: readonly string[];
  narrow?: boolean;
  /** For a field that the refusal describes. */
  id?: string;
  /** What the screen adds under a refusal's sentence, announced with it. */
  children?: ReactNode;
}) {
  const { state, unasked, errors } = useBound("ActionOutcome");
  const code = refusal ?? unasked ?? state.code;

  if (code) {
    return elsewhere.includes(code) ? null : (
      <FailureAlert code={code} errors={errors} narrow={narrow} id={id}>
        {children}
      </FailureAlert>
    );
  }
  if (state.ok && saved !== undefined) {
    return (
      <p role="status" className="text-small text-good">
        {saved}
      </p>
    );
  }
  return null;
}

/**
 * A refusal's sentence as an alert, the one style every form shows one in;
 * for a screen whose request is not an action, `ActionOutcome` otherwise.
 * Without a `code` the failure never reached the API, and `fallback` says it.
 */
export function FailureAlert({
  code,
  errors,
  fallback,
  narrow = false,
  id,
  children,
}: {
  code?: string;
  errors: ErrorSentences;
  /** Said for a code without a sentence, or for no code, in place of the generic one. */
  fallback?: string;
  narrow?: boolean;
  id?: string;
  children?: ReactNode;
}) {
  const sentence =
    code === undefined
      ? (fallback ?? errors.fallback)
      : fallback === undefined
        ? messageForCode(code, errors)
        : (labelFor(errors, code) ?? fallback);
  const width = narrow ? "max-w-40 text-balance" : "max-w-body";
  if (children === undefined || children === null || children === false) {
    return (
      <p id={id} role="alert" className={cn("text-small text-bad", width)}>
        {sentence}
      </p>
    );
  }
  return (
    <div id={id} role="alert" className={cn("flex flex-col gap-1 text-small", width)}>
      <p className="text-bad">{sentence}</p>
      {children}
    </div>
  );
}

/**
 * Offered under one refusal, `code`, that a fresh copy of the page answers:
 * re-reads the page and starts the form over with it, so the refusal goes
 * with what was typed. Not while the form sends again, whose answer the
 * restart would drop.
 */
export function ActionReload({ code, label }: { code: string; label: string }) {
  const { state, pending, restart } = useBound("ActionReload");

  return state.code === code && !pending ? <ReloadButton label={label} onReload={restart} /> : null;
}

/**
 * A form's save row: the button, then how the save went. `children` sit
 * below it, for what only the screen knows about a refusal.
 */
export function ActionFooter({
  labels,
  disabled,
  children,
}: {
  labels: { save: string; saving: string; saved: string };
  disabled?: boolean;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-4">
        <ActionButton disabled={disabled} pendingLabel={labels.saving}>
          {labels.save}
        </ActionButton>
        <ActionOutcome saved={labels.saved} />
      </div>
      {children}
    </div>
  );
}
