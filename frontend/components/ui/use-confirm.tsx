"use client";

import { lazy, Suspense, useCallback, useEffect, useState, type ReactNode } from "react";

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

// The dialog is a chunk of its own, so a screen that never asks (sign-in,
// every plain form) ships none of it.
const loadDialog = () => import("./confirm-dialog");
const ConfirmDialog = lazy(loadDialog);

type Asking = { question: Question; resolve: (yes: boolean) => void };

/**
 * The product's one way of asking first: an in-page dialog whose words come
 * from the dictionary, so the answers are in the interface's language and a
 * name in the question is text, not layout. `ask` resolves true on the answer
 * and false on cancel, Escape or a press outside; render `dialog` once,
 * anywhere in the component. Nothing is mounted until a question is asked;
 * `preload` fetches the dialog once the page has hydrated, so the first
 * question does not wait for it.
 */
export function useConfirm(preload = true): [ask: (question: Question) => Promise<boolean>, dialog: ReactNode] {
  const [asking, setAsking] = useState<Asking | null>(null);

  useEffect(() => {
    if (preload) void loadDialog();
  }, [preload]);

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

  const dialog = asking ? (
    <Suspense fallback={null}>
      <ConfirmDialog question={asking.question} onAnswer={settle} />
    </Suspense>
  ) : null;

  return [ask, dialog];
}
