"use client";

import { useCallback, useEffect, useState, type ReactNode } from "react";

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

type DialogModule = typeof import("./confirm-dialog");

// The dialog is a chunk of its own, so a screen that never asks (sign-in,
// every plain form) ships none of it. A failed load is forgotten, so the
// next question tries again.
let loading: Promise<DialogModule> | null = null;

function loadDialog(): Promise<DialogModule> {
  loading ??= import("./confirm-dialog").catch((error: unknown) => {
    loading = null;
    throw error;
  });
  return loading;
}

type Asking = { question: Question; resolve: (yes: boolean) => void };

/**
 * The product's one way of asking first: an in-page dialog whose words come
 * from the dictionary, so the answers are in the interface's language and a
 * name in the question is text, not layout. `ask` resolves true on the answer
 * and false on cancel, Escape or a press outside, and rejects when the dialog
 * cannot be loaded: nothing was asked, so nothing may be done (`ActionForm`
 * says so; a caller without a place for a sentence reads it as no). Render
 * `dialog` once, anywhere in the component. Nothing is mounted until a
 * question is asked; `preload` fetches the dialog once the page has hydrated,
 * so the first question does not wait for it.
 */
export function useConfirm(preload = true): [ask: (question: Question) => Promise<boolean>, dialog: ReactNode] {
  const [asking, setAsking] = useState<Asking | null>(null);
  const [Dialog, setDialog] = useState<DialogModule["default"] | null>(null);

  useEffect(() => {
    if (!preload) return;
    // A failed preload is retried by the first question, which says so.
    loadDialog().then(
      (loaded) => setDialog(() => loaded.default),
      () => {},
    );
  }, [preload]);

  const ask = useCallback(async (question: Question) => {
    const loaded = await loadDialog();
    setDialog(() => loaded.default);
    return new Promise<boolean>((resolve) => {
      setAsking((previous) => {
        previous?.resolve(false);
        return { question, resolve };
      });
    });
  }, []);

  const settle = (yes: boolean) => {
    asking?.resolve(yes);
    setAsking(null);
  };

  const dialog = asking && Dialog ? <Dialog question={asking.question} onAnswer={settle} /> : null;

  return [ask, dialog];
}
