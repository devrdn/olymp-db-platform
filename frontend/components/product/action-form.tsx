"use client";

import {
  createContext,
  Fragment,
  useActionState,
  useContext,
  useRef,
  type ComponentProps,
  type FormEvent,
  type ReactNode,
} from "react";

import { Button } from "@/components/ui/button";
import { type Question, useConfirm } from "@/components/ui/confirm-dialog";
import type { ActionState } from "@/lib/api/server";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";
import { useHydrated } from "@/lib/use-hydrated";
import { cn } from "@/lib/utils";

type Errors = Pick<Dictionary["errors"], "fallback">;

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
  errors: Errors;
};

const BoundForm = createContext<Bound | null>(null);

function useBound(component: string): Bound {
  const bound = useContext(BoundForm);
  if (!bound) throw new Error(`${component} must sit inside an ActionForm`);
  return bound;
}

/**
 * Whether the enclosing ActionForm's action is running, and whether its last
 * submission worked, for a part that reacts to either (a dialog that closes
 * on success, or may not be dismissed while pending).
 */
export function useActionStatus(): { pending: boolean; ok: boolean } {
  const { state, pending } = useBound("useActionStatus");
  return { pending, ok: state.ok === true };
}

/**
 * A form bound to a server action: the action's state, its hidden `fields`,
 * and, given `confirm`, the destructive form that asks first. Its parts
 * (`ActionButton`, `ActionOutcome`, `ActionFooter`) read the binding, so a
 * screen lays them out and never handles pending, hydration or a refusal's
 * code itself. `children` may be a function of the state, for a screen that
 * shows what the action returned.
 */
export function ActionForm<S extends ActionState>({
  action,
  fields = {},
  confirm,
  errors,
  revision,
  onSubmit,
  className,
  noValidate,
  children,
}: {
  action: (previous: Awaited<S>, form: FormData) => Promise<S>;
  fields?: Record<string, string | number | readonly string[]>;
  confirm?: Confirmation;
  errors: Errors;
  /**
   * The version of the server's copy the fields show. A new one remounts the
   * form's contents, so uncontrolled fields take the saved values, while how
   * the save went stays on screen; a `key` on the form would forget both.
   */
  revision?: string | number;
  /** Runs before the question; preventing the event sends nothing and asks nothing. */
  onSubmit?: (event: FormEvent<HTMLFormElement>) => void;
  className?: string;
  noValidate?: boolean;
  children: ReactNode | ((state: Awaited<S>, pending: boolean) => ReactNode);
}) {
  // Every state starts empty: nothing sent yet.
  const [state, formAction, pending] = useActionState<S, FormData>(action, {} as Awaited<S>);
  const hydrated = useHydrated();
  // React replays a submission queued before hydration without `onSubmit`,
  // where the question is asked, so a form that asks cannot be sent until then.
  const locked = confirm !== undefined && !hydrated;
  const [ask, dialog] = useConfirm();
  // Set by a yes, for the one submission it lets through.
  const approved = useRef(false);

  return (
    <BoundForm.Provider value={{ state, pending, locked, errors }}>
      <form
        action={formAction}
        className={className}
        noValidate={noValidate}
        onSubmit={(event) => {
          onSubmit?.(event);
          if (event.defaultPrevented || confirm === undefined) return;
          if (approved.current) {
            approved.current = false;
            return;
          }
          const form = event.currentTarget;
          const question = typeof confirm === "function" ? confirm(new FormData(form)) : confirm;
          if (question === null) return;
          event.preventDefault();
          void ask(question).then((yes) => {
            if (!yes) return;
            approved.current = true;
            form.requestSubmit();
          });
        }}
      >
        {Object.entries(fields).flatMap(([name, value]) =>
          (Array.isArray(value) ? value : [String(value)]).map((one, index) => (
            <input key={`${name}-${index}`} type="hidden" name={name} value={one} />
          )),
        )}
        <Fragment key={revision}>{typeof children === "function" ? children(state, pending) : children}</Fragment>
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
  const { state, errors } = useBound("ActionOutcome");
  const code = refusal ?? state.code;

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
 */
export function FailureAlert({
  code,
  errors,
  narrow = false,
  id,
  children,
}: {
  code: string;
  errors: Errors;
  narrow?: boolean;
  id?: string;
  children?: ReactNode;
}) {
  const width = narrow ? "max-w-40 text-balance" : "max-w-body";
  if (children === undefined || children === null || children === false) {
    return (
      <p id={id} role="alert" className={cn("text-small text-bad", width)}>
        {messageForCode(code, errors)}
      </p>
    );
  }
  return (
    <div id={id} role="alert" className={cn("flex flex-col gap-1 text-small", width)}>
      <p className="text-bad">{messageForCode(code, errors)}</p>
      {children}
    </div>
  );
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
