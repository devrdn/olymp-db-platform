"use client";

import { useActionState, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { messageForCode } from "@/lib/i18n/errors";

import { resetDatabaseAction, type ResetState } from "./actions";
import type { PlayDictionary } from "./dictionary";
import { showsReference } from "./refusals";

const IDLE: ResetState = { kind: "idle" };

/**
 * The console's "reset my database" control, offered only by a writing
 * contest. A reset drops everything the participant changed, so it asks first;
 * the dialog stays open while the copy is made and then says how it went.
 */
export function ResetDatabase({
  contestId,
  onReset,
  dict,
}: {
  contestId: string;
  /** Called after each successful reset, so the screen re-reads what the database shows. */
  onReset: () => void;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace.reset;
  const [open, setOpen] = useState(false);
  const [state, formAction, pending] = useActionState<ResetState, FormData>(resetDatabaseAction, IDLE);
  // The state when the dialog was last opened: an outcome is shown only if it
  // came after, so reopening asks again.
  const [askedAt, setAskedAt] = useState<ResetState>(IDLE);
  const outcome = state !== askedAt && state.kind !== "idle" ? state : null;

  const onResetRef = useRef(onReset);
  useEffect(() => {
    onResetRef.current = onReset;
  });
  // Every action call returns a new object, so this runs once per success.
  useEffect(() => {
    if (state.kind === "done") onResetRef.current();
  }, [state]);

  return (
    <>
      <Button
        type="button"
        variant="quiet"
        size="sm"
        onClick={() => {
          setAskedAt(state);
          setOpen(true);
        }}
      >
        {t.open}
      </Button>

      <Dialog open={open} onOpenChange={setOpen} dismissible={!pending}>
        <DialogContent closeLabel={t.close} dismissible={!pending}>
          <DialogHeader>
            <DialogTitle>{t.title}</DialogTitle>
            <DialogDescription>{t.body}</DialogDescription>
          </DialogHeader>
          <p className="text-small text-ink-2">{t.kept}</p>

          {outcome?.kind === "done" ? (
            <p role="status" className="text-body text-ink">
              {t.done}
            </p>
          ) : null}
          {outcome?.kind === "refused" ? (
            <div role="alert" className="border border-bad/40 bg-bad-wash p-3 text-body text-ink">
              {messageForCode(outcome.code, dict.errors)}
              {outcome.requestId && showsReference(outcome.code, dict.errors) ? (
                <p className="mt-2 font-mono text-small text-ink-3">
                  {dict.participant.console.reference.replace("{id}", outcome.requestId)}
                </p>
              ) : null}
            </div>
          ) : null}

          {outcome !== null ? (
            <DialogFooter>
              <Button type="button" variant="secondary" onClick={() => setOpen(false)}>
                {t.close}
              </Button>
            </DialogFooter>
          ) : (
            <form action={formAction}>
              <input type="hidden" name="contestId" value={contestId} />
              <DialogFooter>
                <Button type="button" variant="secondary" disabled={pending} onClick={() => setOpen(false)}>
                  {t.cancel}
                </Button>
                <Button type="submit" variant="danger" disabled={pending}>
                  {pending ? t.resetting : t.confirm}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
