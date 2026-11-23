"use client";

import { useCallback, useEffect, useState, type ReactNode } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

/** A question asked before a step that cannot be taken back; every word is the caller's. */
export type Question = {
  /** What is asked. */
  title: string;
  /** What the step costs, under the question. */
  detail?: string;
  /** The button that goes ahead, named for the step. */
  answer: string;
  /** The button, and the corner control, that send nothing. */
  cancel: string;
  /** The answer is drawn as a destructive step. */
  danger?: boolean;
};

/**
 * How long the answer stays disabled once the question is on screen. The
 * dialog opens where the pointer may already be, so the second click of a
 * double-click on the control that asked can land on the answer.
 */
export const CONFIRM_ARM_MS = 400;

type Asking = { question: Question; resolve: (yes: boolean) => void };

/**
 * False for `CONFIRM_ARM_MS` after the calling component mounts, then true:
 * an answer mounted with its question is pressable only once armed.
 */
export function useArmed(): boolean {
  const [armed, setArmed] = useState(false);

  useEffect(() => {
    const timer = setTimeout(() => setArmed(true), CONFIRM_ARM_MS);
    return () => clearTimeout(timer);
  }, []);

  return armed;
}

/**
 * The product's one way of asking first: an in-page dialog whose words come
 * from the dictionary, so the answers are in the interface's language and a
 * name in the question is text, not layout. `ask` resolves true on the answer
 * and false on cancel, Escape or a press outside; render `dialog` once,
 * anywhere in the component.
 */
export function useConfirm(): [ask: (question: Question) => Promise<boolean>, dialog: ReactNode] {
  const [asking, setAsking] = useState<Asking | null>(null);

  const ask = useCallback(
    (question: Question) =>
      new Promise<boolean>((resolve) => {
        setAsking((previous) => {
          previous?.resolve(false);
          return { question, resolve };
        });
      }),
    [],
  );

  const settle = (yes: boolean) => {
    asking?.resolve(yes);
    setAsking(null);
  };

  const dialog = (
    <Dialog
      open={asking !== null}
      onOpenChange={(open) => {
        if (!open) settle(false);
      }}
    >
      {/* Mounted only while asking, so every question starts unarmed. */}
      {asking ? <Asked question={asking.question} onAnswer={settle} /> : null}
    </Dialog>
  );

  return [ask, dialog];
}

function Asked({ question, onAnswer }: { question: Question; onAnswer: (yes: boolean) => void }) {
  const armed = useArmed();

  return (
    <DialogContent closeLabel={question.cancel}>
      <DialogHeader>
        <DialogTitle>{question.title}</DialogTitle>
        {question.detail ? <DialogDescription>{question.detail}</DialogDescription> : null}
      </DialogHeader>
      <DialogFooter>
        <Button type="button" variant="quiet" onClick={() => onAnswer(false)}>
          {question.cancel}
        </Button>
        <Button
          type="button"
          variant={question.danger ? "danger" : "primary"}
          disabled={!armed}
          onClick={() => onAnswer(true)}
        >
          {question.answer}
        </Button>
      </DialogFooter>
    </DialogContent>
  );
}
